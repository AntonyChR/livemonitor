package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"live-recorder/internal/app"
	"live-recorder/internal/domain"
	"live-recorder/utils"
	"math/rand"
	"net"
	"os"
	"sync"
)

type UnixSocketServer struct {
	appService app.Service
	ctx        context.Context
	socketPath string
	mu         sync.RWMutex
	clients    map[net.Conn]chan domain.GlobalStatus
}

func NewUnixSocketServer(ctx context.Context, appService app.Service, socketPath string) *UnixSocketServer {
	return &UnixSocketServer{
		appService: appService,
		ctx:        ctx,
		socketPath: socketPath,
		clients:    make(map[net.Conn]chan domain.GlobalStatus),
	}
}

var words = []string{"blue", "black", "moon", "xxx", "echo", "alpha", "delta"}

func generateUnixSocketName() string {
	word := words[rand.Intn(len(words))]

	letters := make([]byte, 3)
	for i := range letters {
		letters[i] = byte('a' + rand.Intn(26))
	}

	return word + "_" + string(letters) + ".sock"
}

func (u *UnixSocketServer) registerClient(conn net.Conn) chan domain.GlobalStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	ch := make(chan domain.GlobalStatus, 64)
	u.clients[conn] = ch
	return ch
}

func (u *UnixSocketServer) unregisterClient(conn net.Conn) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if ch, ok := u.clients[conn]; ok {
		delete(u.clients, conn)
		close(ch)
	}
}

func (u *UnixSocketServer) broadcast(status domain.GlobalStatus) {
	u.mu.RLock()
	defer u.mu.RUnlock()

	if len(u.clients) == 0 {
		return
	}

	for _, ch := range u.clients {
		select {
		case ch <- status:
		default:
			// Buffer lleno para este cliente; se descarta el evento para no bloquear a otros
		}
	}
}

func (u *UnixSocketServer) Start() error {

	socketName := generateUnixSocketName()

	if err := os.Remove(u.socketPath); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Error removing old socket: %s\n", err)
		return err
	}
	utils.SetTempUnixServerName(socketName)
	listener, err := net.Listen("unix", u.socketPath)
	if err != nil {
		fmt.Printf("Error listening on socket: %s\n", err)
		return err
	}

	defer listener.Close()

	go func() {
		<-u.ctx.Done()
		listener.Close()
		if err = os.Remove(u.socketPath); err != nil {
			fmt.Printf("Error removing unix socket: %s\n", err)
			return
		}
		fmt.Printf("unix socket %s removed\n", u.socketPath)
	}()

	fmt.Printf("Listening on unix socket: %s\n", u.socketPath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if u.ctx.Err() != nil {
				return nil
			}
			fmt.Printf("Accept error: %v\n", err)
			continue
		}
		clientCh := u.registerClient(conn)
		go u.handleConn(conn)
		go u.sendAppEventsToClientSocket(conn, clientCh)
	}
}

type Command int

const (
	GetGlobalStatus Command = iota
	GetUserStatus
	SetAutoRecord
	StartRecording
	StopRecording
	StartMonitoring
	Shutdown
	Subscribe
	ReloadUsers
	Summary
	Echo
)

func (c Command) string() string {
	strings := []string{
		"GetGlobalStatus",
		"GetUserStatus",
		"SetAutoRecord",
		"StartRecording",
		"StopRecording",
		"StartMonitoring",
		"Shutdown",
		"Subscribe",
		"ReloadUsers",
		"Summary",
		"Echo",
	}

	if c > Echo || c < GetGlobalStatus {
		return fmt.Sprintf("invalid command: %d", c)
	}
	return strings[c]
}

type Req struct {
	Type    Command `json:"type"`
	UserdID string  `json:"user_id"`
	AutoRecord bool `json:"auto_record"`
}

func (u *UnixSocketServer) handleConn(conn net.Conn) {
	println("New socket connection")
	buf := make([]byte, 512)
	defer conn.Close()
	defer u.unregisterClient(conn)

Loop:
	for {
		select {
		case <-u.ctx.Done():
			break Loop
		default:
			n, err := conn.Read(buf)
			if err != nil || n == 0 {
				println("error reading buffer")
				break Loop
			}
			msg := buf[:n]
			cleanText := bytes.ReplaceAll(msg, []byte{0}, []byte{})
			println(string(cleanText))
			var req Req
			if err = json.Unmarshal(cleanText, &req); err != nil {
				fmt.Printf("error unmarshalling request %s\n", err)
			}

			fmt.Printf("command: %s\n", req.Type.string())

			switch req.Type {
			case GetGlobalStatus:
				globalStatus := u.appService.GetGlobalStatus(context.Background())
				data, err := json.Marshal(globalStatus)
				if err != nil {
					fmt.Printf("error marshalling global status %s\n", err)
					continue
				}
				if n, err = conn.Write(data); err != nil || n == 0 {
					fmt.Printf("error writing in unix socket %s\n", err)
				}
			case SetAutoRecord:
				err = u.appService.SetAutoRecord(context.Background(), req.UserdID, req.AutoRecord)
				if err != nil {
					fmt.Printf("error setting autorecord for user%s, error %s\n", req.UserdID, err)
				}
			case StartRecording:
				if _, err := u.appService.StartRecording(context.Background(), req.UserdID); err != nil {
					fmt.Printf("error start recording %s\n", err)
				}
			case StopRecording:
				if err = u.appService.StopRecording(context.Background(), req.UserdID); err != nil {
					fmt.Printf("error stop recording %s\n", err)
				}
			case Echo:
				if n, err = conn.Write([]byte("echo\n")); err != nil || n == 0 {
					fmt.Printf("error writing in unix socket %s\n", err)
				}
			default:
				println("no command handled: ", req.Type.string())
			}
		}
	}
	println("closing socket connection")
}

func (u *UnixSocketServer) sendAppEventsToClientSocket(conn net.Conn, clientCh chan domain.GlobalStatus) {
	defer conn.Close()
	defer u.unregisterClient(conn)

Loop:
	for {
		select {
		case <-u.ctx.Done():
			break Loop
		case event, ok := <-clientCh:
			if !ok {
				break Loop
			}
			data, err := json.Marshal(event)
			if err != nil {
				fmt.Printf("error marshalling data %s\n", err)
				continue
			}

			if n, err := conn.Write(data); err != nil || n == 0 {
				fmt.Printf("error writing in unix socket %s\n", err)
				break Loop
			}
		}
	}
}

func (u *UnixSocketServer) ListenDomainEvents(eventCh <-chan app.Event) {
	for {
		select {
		case <-u.ctx.Done():
			return
		case ev, ok := <-eventCh:
			if !ok {
				return
			}
			switch e := ev.(type) {
			case app.UserStatusChangedEvent:
				if e.Current.LiveState != e.Previous.LiveState || e.Current.RecordingState != e.Previous.RecordingState{
					if e.Current.RecordingState != domain.RecordingStateRecording {
						user := domain.UserStatus{
							Alias: e.Current.Alias,
							RecordingState: e.Current.RecordingState,
							LiveState: e.Current.LiveState,
							ID: e.Current.ID,
						}
						u.broadcast(domain.GlobalStatus{Users: []domain.UserStatus{user}})
					}
				}
			case app.RecordingStartedEvent:
				u.broadcast(domain.GlobalStatus{ActiveRecordings: []domain.RecordingSessionInfo{e.Session}})
			case app.RecordingStoppingEvent:
				//TODO
			case app.RecordingStoppedEvent:
				//TODO
			case app.RecordingFailedEvent:
				//TODO
			case app.MonitorErrorEvent:
				//TODO
			case app.ConfigurationChangedEvent:
				var users []domain.UserStatus
				for _, u := range e.Users {
					users = append(users, domain.UserStatus{
						ID:                u.ID,
						Alias:             u.Alias,
						AutoRecordEnabled: u.ShouldRecord,
					})
				}
				u.broadcast(domain.GlobalStatus{Users: users})
			default:
				println("no handled event: ", e.Type())
			}
		}
	}
}

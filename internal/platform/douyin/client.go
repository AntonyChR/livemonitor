package douyin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client defines the interface for interacting with Douyin live stream APIs.
type Client interface {
	GetLiveStatus(ctx context.Context, userID string) (LiveStatus, error)
	GetLiveURL(ctx context.Context, roomID string) (string, error)
}

type douyinClient struct {
	httpClient *http.Client
	baseURL    string
	timeout    time.Duration
}

// ClientOption configures the Douyin client.
type ClientOption func(*douyinClient)

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *douyinClient) {
		c.httpClient = httpClient
	}
}

// WithBaseURL overrides the base URL (useful for testing).
func WithBaseURL(baseURL string) ClientOption {
	return func(c *douyinClient) {
		c.baseURL = strings.TrimRight(baseURL, "/")
	}
}

// NewClient creates a new Douyin client with given timeout and options.
func NewClient(timeout time.Duration, opts ...ClientOption) Client {
	c := &douyinClient{
		httpClient: &http.Client{Timeout: timeout},
		baseURL:    "https://live.douyin.com",
		timeout:    timeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *douyinClient) applyHeaders(req *http.Request) {
	req.Header.Set("accept", "application/json, text/plain, */*")
	req.Header.Set("accept-language", "es-419,es;q=0.9,es-ES;q=0.8,en;q=0.7,en-GB;q=0.6,en-US;q=0.5,es-PE;q=0.4")
	req.Header.Set("origin", "https://www.douyin.com")
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", "https://www.douyin.com/")
	req.Header.Set("sec-ch-ua", `"Chromium";v="154", "Microsoft Edge";v="154", "Not A(Brand";v="99"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Linux"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-site")
	req.Header.Set("user-agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36 Edg/154.0.0.0")
}

func (c *douyinClient) doRequest(ctx context.Context, endpointURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error al construir solicitud: %w", err)
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error en solicitud HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error al leer respuesta: %w", err)
	}
	return body, nil
}

// ParseLiveStatus parses the raw JSON response to extract the live status for a specific user.
func ParseLiveStatus(body []byte, userID string) (LiveStatus, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return LiveStatus{}, fmt.Errorf("respuesta de la API inválida: se esperaba un objeto: %w", err)
	}

	statusCodeVal, ok := root["status_code"]
	if !ok {
		return LiveStatus{}, fmt.Errorf("respuesta de estado inválida: status_code ausente")
	}
	statusCode, err := toInt(statusCodeVal)
	if err != nil || statusCode != 0{
		return LiveStatus{}, fmt.Errorf("respuesta de estado inválida")
	}

	dataVal, ok := root["data"]
	if !ok {
		return LiveStatus{}, fmt.Errorf("respuesta de estado inválida: data ausente")
	}
	dataList, ok := dataVal.([]any)
	if !ok {
		return LiveStatus{}, fmt.Errorf("respuesta de estado inválida: data no es array")
	}

	for _, entryVal := range dataList {
		entry, ok := entryVal.(map[string]any)
		if !ok {
			continue
		}
		usersVal, ok := entry["user_live"]
		if !ok {
			return LiveStatus{}, fmt.Errorf("lista de usuarios inválida")
		}
		usersList, ok := usersVal.([]any)
		if !ok {
			return LiveStatus{}, fmt.Errorf("lista de usuarios inválida")
		}

		for _, itemVal := range usersList {
			live, ok := itemVal.(map[string]any)
			if !ok {
				continue
			}

			liveUserID, err := extractIdentifier(live, "user_id_str", "user_id")
			if err != nil || liveUserID != userID {
				continue
			}

			statusStr, err := extractStringOrInt(live, "live_status")
			if err != nil {
				return LiveStatus{}, fmt.Errorf("estado de directo desconocido: %w", err)
			}

			switch statusStr {
			case "1":
				roomID, err := extractIdentifier(live, "room_id_str", "room_id")
				if err != nil {
					return LiveStatus{}, fmt.Errorf("room_id inválido en respuesta: %w", err)
				}
				return LiveStatus{Status: StatusLive, RoomID: roomID}, nil
			case "0":
				return LiveStatus{Status: StatusOffline}, nil
			default:
				return LiveStatus{}, fmt.Errorf("estado de directo desconocido: %s", statusStr)
			}
		}
	}

	return LiveStatus{}, fmt.Errorf("la respuesta no contiene al usuario consultado")
}

func (c *douyinClient) GetLiveStatus(ctx context.Context, userID string) (LiveStatus, error) {
	reqURL := fmt.Sprintf("%s/webcast/distribution/check_user_live_status/?user_ids=%s", c.baseURL, url.QueryEscape(userID))
	body, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return LiveStatus{}, err
	}
	return ParseLiveStatus(body, userID)
}

func (c *douyinClient) GetLiveURL(ctx context.Context, roomID string) (string, error) {
	params := url.Values{
		"aid":                 {"6383"},
		"app_name":            {"douyin_web"},
		"live_id":             {"1"},
		"device_platform":     {"web"},
		"language":            {"en-US"},
		"cookie_enabled":      {"true"},
		"screen_width":        {"1680"},
		"screen_height":       {"1050"},
		"browser_language":    {"en-US"},
		"browser_platform":    {"Linux x86_64"},
		"browser_name":        {"Brave"},
		"browser_version":     {"154.0.0.0"},
		"os_name":             {"Linux"},
		"os_version":          {"web"},
		"room_id":             {roomID},
		"scene":               {"pc_profile_struct"},
		"channel":             {"channel_pc_web"},
		"region":              {"cn"},
		"device_type":         {"web_device"},
		"version_code":        {"170400"},
		"webcast_sdk_version": {"2450"},
		"ignoreToast":         {"true"},
	}

	reqURL := fmt.Sprintf("%s/webcast/room/info_by_scene/?%s", c.baseURL, params.Encode())
	body, err := c.doRequest(ctx, reqURL)
	if err != nil {
		return "", err
	}

	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", fmt.Errorf("error al decodificar respuesta JSON: %w", err)
	}

	dataVal, ok := root["data"]
	if !ok || dataVal == nil {
		return "", nil
	}
	data, ok := dataVal.(map[string]any)
	if !ok {
		return "", nil
	}

	ownerVal, ok := data["owner"]
	if !ok || ownerVal == nil {
		return "", nil
	}
	owner, ok := ownerVal.(map[string]any)
	if !ok {
		return "", nil
	}

	webRidVal, ok := owner["web_rid"]
	if !ok || webRidVal == nil {
		return "", nil
	}

	webRid, err := toIdentifier(webRidVal)
	if err != nil || webRid == "" {
		return "", nil
	}

	return fmt.Sprintf("https://live.douyin.com/%s", webRid), nil
}

func extractIdentifier(obj map[string]any, preferredKey, fallbackKey string) (string, error) {
	if val, ok := obj[preferredKey]; ok && val != nil {
		id, err := toIdentifier(val)
		if err == nil && id != "" {
			return id, nil
		}
	}
	if val, ok := obj[fallbackKey]; ok && val != nil {
		id, err := toIdentifier(val)
		if err == nil && id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("identificador ausente o inválido en la respuesta de la API")
}

func toIdentifier(val any) (string, error) {
	switch v := val.(type) {
	case string:
		v = strings.TrimSpace(v)
		if isAllDigits(v) {
			return v, nil
		}
	case float64:
		if v >= 0 && v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), nil
		}
	case json.Number:
		str := v.String()
		if isAllDigits(str) {
			return str, nil
		}
	}
	return "", fmt.Errorf("identificador inválido: %v", val)
}

func extractStringOrInt(obj map[string]any, key string) (string, error) {
	val, ok := obj[key]
	if !ok || val == nil {
		return "", fmt.Errorf("campo %s ausente", key)
	}
	switch v := val.(type) {
	case string:
		return v, nil
	case float64:
		return strconv.Itoa(int(v)), nil
	case int:
		return strconv.Itoa(v), nil
	case json.Number:
		return v.String(), nil
	default:
		return "", fmt.Errorf("tipo no esperado para %s: %T", key, val)
	}
}

func toInt(val any) (int, error) {
	switch v := val.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case string:
		return strconv.Atoi(v)
	case json.Number:
		n, err := v.Int64()
		return int(n), err
	default:
		return 0, fmt.Errorf("no se puede convertir %T a int", val)
	}
}

func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

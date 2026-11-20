package douyin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseLiveStatus(t *testing.T) {
	t.Run("selecciona la sala del usuario coincidente", func(t *testing.T) {
		jsonResp := `{
			"status_code": 0,
			"data": [
				{
					"user_live": [
						{"user_id_str": "111", "live_status": 1, "room_id_str": "999"},
						{"user_id_str": "222", "live_status": "1", "room_id_str": "888"}
					]
				}
			]
		}`
		status, err := ParseLiveStatus([]byte(jsonResp), "222")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if status.Status != StatusLive {
			t.Errorf("expected status live, got %s", status.Status)
		}
		if status.RoomID != "888" {
			t.Errorf("expected room ID 888, got %s", status.RoomID)
		}
	})

	t.Run("distingue offline de respuesta incompleta o desconocida", func(t *testing.T) {
		offlineResp := `{
			"status_code": 0,
			"data": [
				{
					"user_live": [
						{"user_id_str": "123", "live_status": 0}
					]
				}
			]
		}`
		status, err := ParseLiveStatus([]byte(offlineResp), "123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if status.Status != StatusOffline {
			t.Errorf("expected status offline, got %s", status.Status)
		}

		invalidBodies := []string{
			"",
			"null",
			"{}",
			`{"status_code": 0, "data": []}`,
			`{"status_code": 0, "data": [{"user_live": [{"user_id_str": "123", "live_status": 4}]}]}`,
			`{"status_code": 0, "data": [{"user_live": [{"user_id_str": "123", "live_status": 1}]}]}`, // missing room_id
			`{"status_code": 1, "data": []}`,
		}

		for _, body := range invalidBodies {
			_, err := ParseLiveStatus([]byte(body), "123")
			if err == nil {
				t.Errorf("expected error for body %q, got nil", body)
			}
		}
	})
}

func TestClient_GetLiveURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		roomID := r.URL.Query().Get("room_id")
		if roomID == "12345" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"data": {
					"owner": {
						"web_rid": "68833110264"
					}
				}
			}`))
			return
		}
		if roomID == "no_owner" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data": {"owner": null}}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(5*time.Second, WithBaseURL(server.URL), WithHTTPClient(server.Client()))

	ctx := context.Background()
	liveURL, err := client.GetLiveURL(ctx, "12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "https://live.douyin.com/68833110264"
	if liveURL != expected {
		t.Errorf("expected %s, got %s", expected, liveURL)
	}

	liveURL, err = client.GetLiveURL(ctx, "no_owner")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if liveURL != "" {
		t.Errorf("expected empty live URL, got %s", liveURL)
	}
}

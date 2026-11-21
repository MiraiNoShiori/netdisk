package handler

import (
	"bytes"
	"context"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MiraiNoShiori/netdisk/internal/middleware"
	"github.com/google/uuid"
)

type uploadTestUser struct{}

func (uploadTestUser) Authenticate(context.Context, string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func TestUploadRejectsOversizedFields(t *testing.T) {
	for _, key := range []string{"unknown", "name", "parent_id"} {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			m := multipart.NewWriter(&buf)
			if key == "unknown" {
				if err := m.WriteField(key, strings.Repeat("a", maxDiscardBytes/2)); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.WriteField(key, strings.Repeat("b", 2*maxFieldBytes)); err != nil {
				t.Fatal(err)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			size := buf.Len()
			r := httptest.NewRequest("POST", "/api/v1/files/", &buf)
			r.Header.Set("Content-Type", m.FormDataContentType())
			r.Header.Set("Authorization", "Bearer test")
			s := Server{deps: Deps{Logger: slog.Default()}}
			w := httptest.NewRecorder()
			middleware.RequireAuth(s.deps.Logger, uploadTestUser{})(http.HandlerFunc(s.uploadFile)).ServeHTTP(w, r)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "超过") || size-buf.Len() > maxDiscardBytes+8192 {
				t.Fatalf("status=%d body=%s bytes read=%d", w.Code, w.Body.String(), size-buf.Len())
			}
		})
	}
}

func TestDrainPartsStopsAtBudget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count, size int
	}{{"bytes", 1, 2 * maxDiscardBytes}, {"parts", maxTrailingParts + 1, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			m := multipart.NewWriter(&buf)
			for i := 0; i < tc.count; i++ {
				n := tc.size
				if i == maxTrailingParts {
					n = 2 * maxDiscardBytes
				}
				if err := m.WriteField("tail", strings.Repeat("x", n)); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			drainParts(multipart.NewReader(&buf, m.Boundary()), maxDiscardBytes)
			if buf.Len() < maxDiscardBytes-8192 {
				t.Fatalf("only %d bytes left unread", buf.Len())
			}
		})
	}
}

func TestContentDispositionUnicodeFallback(t *testing.T) {
	for _, mode := range []string{"inline", "attachment"} {
		got := contentDispositionMode(mode, "图 片.pdf")
		if got != mode+`; filename="- -.pdf"; filename*=UTF-8''%E5%9B%BE%20%E7%89%87.pdf` {
			t.Fatal(got)
		}
	}
}

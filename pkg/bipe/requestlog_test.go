package bipe

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
)

func TestNoRequestBodiesInLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"x"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	for _, path := range []string{"/ok", "/fail"} {
		var buf bytes.Buffer
		cli, err := mautrix.NewClient(srv.URL, "@a:bipe.net.br", "tok")
		if err != nil {
			t.Fatal(err)
		}
		cli.Log = zerolog.New(&buf).Level(zerolog.TraceLevel)
		NoRequestBodiesInLog(cli)
		ctx := cli.Log.WithContext(context.Background())
		_, _ = cli.MakeRequest(ctx, http.MethodPost, srv.URL+path, map[string]string{"text": "segredo-do-cliente"}, nil)
		out := buf.String()
		if strings.Contains(out, "segredo-do-cliente") || strings.Contains(out, "req_body") {
			t.Fatalf("%s: the log holds the request body: %s", path, out)
		}
		if !strings.Contains(out, `"method":"POST"`) || !strings.Contains(out, `"status_code"`) {
			t.Fatalf("%s: the log lost the method or the status: %s", path, out)
		}
	}
}

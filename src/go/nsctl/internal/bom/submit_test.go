package bom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// skip_async decides who runs the deployment, so the two modes have to be
// distinguishable at the wire.
func TestApplyChangeSetCarriesTheChosenSkipAsync(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		skip bool
	}{
		{"the caller runs it", true},
		{"the control plane runs it", false},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"csd_nid":"csd-1"}`))
			}))
			defer srv.Close()

			c := msdeploy.New(srv.URL)
			nid, err := c.ApplyChangeSet(context.Background(), "cs", "ds", tt.skip)
			if err != nil {
				t.Fatalf("ApplyChangeSet: %v", err)
			}
			if nid != "csd-1" {
				t.Errorf("csd_nid = %q, want csd-1", nid)
			}
			if got["skip_async"] != tt.skip {
				t.Errorf("skip_async = %v, want %v", got["skip_async"], tt.skip)
			}
		})
	}
}

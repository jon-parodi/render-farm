package farm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jon-parodi/render-farm/rt"
)

// A lease that times out must go to the next worker that asks, and a late
// result from the original worker must not be counted twice.
func TestExpiredLeaseIsRequeued(t *testing.T) {
	c := NewCoordinator(t.TempDir())
	c.leaseTimeout = 20 * time.Millisecond
	call := func(h http.HandlerFunc, url string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("POST", url, bytes.NewReader(body)))
		return w
	}
	if w := call(c.handleRender, "/api/render", []byte(`{"scene":"cornell","w":64,"h":64,"spp":1}`)); w.Code != 200 {
		t.Fatalf("render: %d %s", w.Code, w.Body)
	}
	lease := func(node string) (l Lease, code int) {
		w := call(c.handleLease, "/internal/lease?node="+node+"&pod="+node, nil)
		json.Unmarshal(w.Body.Bytes(), &l)
		return l, w.Code
	}
	a, _ := lease("a")
	if _, code := lease("b"); code != http.StatusNoContent {
		t.Fatalf("the only tile is leased to a, but b got status %d", code)
	}
	time.Sleep(40 * time.Millisecond)
	b, code := lease("b")
	if code != 200 || b.Task != a.Task {
		t.Fatalf("expired lease not handed to b: status %d, task %d", code, b.Task)
	}
	body := make([]byte, 64*64*rt.Channels*4)
	for _, node := range []string{"b", "a"} {
		url := "/internal/result?node=" + node + "&pod=" + node + "&task=0&job=" + strconv.FormatInt(c.job.ID, 10)
		// b's result completes the job; a's late duplicate is turned away
		if w := call(c.handleResult, url, body); w.Code != map[string]int{"b": 200, "a": http.StatusGone}[node] {
			t.Fatalf("result from %s: %d", node, w.Code)
		}
	}
	if c.job.State != "done" || c.job.tileSPP[0] != 1 || c.expired != 1 {
		t.Fatalf("state %s, tile spp %d, expired %d; want done, 1, 1", c.job.State, c.job.tileSPP[0], c.expired)
	}
}

package farm

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount/"

// killWorker deletes one random running worker pod through the Kubernetes API,
// using the pod's own namespace-scoped ServiceAccount. It talks to the API
// directly: two requests don't justify a client library.
func killWorker() (pod, node string, err error) {
	token, err1 := os.ReadFile(saDir + "token")
	ca, err2 := os.ReadFile(saDir + "ca.crt")
	ns, err3 := os.ReadFile(saDir + "namespace")
	if err1 != nil || err2 != nil || err3 != nil {
		return "", "", fmt.Errorf("not running in a cluster (no service account)")
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	base := "https://kubernetes.default.svc/api/v1/namespaces/" + strings.TrimSpace(string(ns)) + "/pods"
	do := func(method, u string, out any) error {
		req, _ := http.NewRequest(method, u, nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("kubernetes API: %s %s", method, resp.Status)
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	selector := os.Getenv("WORKER_SELECTOR")
	if selector == "" {
		return "", "", fmt.Errorf("WORKER_SELECTOR is not set")
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string
				DeletionTimestamp *string
			}
			Spec   struct{ NodeName string }
			Status struct{ Phase string }
		}
	}
	if err := do("GET", base+"?labelSelector="+url.QueryEscape(selector), &list); err != nil {
		return "", "", err
	}
	var live []int
	for i, p := range list.Items {
		if p.Status.Phase == "Running" && p.Metadata.DeletionTimestamp == nil {
			live = append(live, i)
		}
	}
	if len(live) == 0 {
		return "", "", fmt.Errorf("no running worker pods to kill")
	}
	p := list.Items[live[rand.IntN(len(live))]]
	// grace period 0: die mid-tile, like a crashed node would
	if err := do("DELETE", base+"/"+url.PathEscape(p.Metadata.Name)+"?gracePeriodSeconds=0", nil); err != nil {
		return "", "", err
	}
	return p.Metadata.Name, p.Spec.NodeName, nil
}

func (c *Coordinator) handleKill(w http.ResponseWriter, r *http.Request) {
	pod, node, err := killWorker()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	log.Printf("killed worker pod %s on %s", pod, node)
	c.mu.Lock()
	c.kills++
	c.broadcast("killed", map[string]string{"pod": pod, "node": node})
	c.mu.Unlock()
	writeJSON(w, map[string]string{"pod": pod, "node": node})
}

package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dxmed/internal/dbtest"
	"dxmed/internal/obs"
	"dxmed/internal/ops"
	"dxmed/internal/store"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type env struct {
	mu    sync.Mutex
	vars  map[string]string
	addrs map[string]string
	ready chan struct{}
}

func newEnv(t *testing.T, vars map[string]string) (*env, Env) {
	t.Helper()
	dbtest.Open(t) // skips without a database
	v := &env{vars: vars, addrs: map[string]string{}, ready: make(chan struct{}, 4)}
	return v, Env{
		Getenv: func(k string) string { return v.vars[k] },
		Out:    io.Discard,
		Open:   func(ctx context.Context) (*store.Store, error) { return dbtest.Open(t), nil },
		OnListen: func(name, addr string) {
			v.mu.Lock()
			v.addrs[name] = addr
			v.mu.Unlock()
			v.ready <- struct{}{}
		},
	}
}

func (v *env) addr(name string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.addrs[name]
}

func start(t *testing.T, name string, e Env) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, name, e) }()
	return cancel, done
}

func wait(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("service did not stop")
		return nil
	}
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEngineAndCollector(t *testing.T) {
	dbtest.Truncate(t)
	st := dbtest.Open(t)
	root := t.TempDir()
	if _, err := ops.Seed(context.Background(), st, root, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"med-engine", "med-collector"} {
		v, e := newEnv(t, map[string]string{"MED_METRICS_ADDR": "127.0.0.1:0", "MED_BEAT_EVERY": "50ms", "MED_POLL": "20ms",
			"MED_TICK_EVERY": "50ms", "MED_SCAN_EVERY": "20ms", "MED_WORKERS": "1"})
		cancel, done := start(t, name, e)
		<-v.ready
		code, body := get(t, http.DefaultClient, "http://"+v.addr("metrics")+"/metrics")
		if code != 200 {
			t.Fatal(name, code, body)
		}
		if c, _ := get(t, http.DefaultClient, "http://"+v.addr("metrics")+"/healthz"); c != 200 {
			t.Fatal(c)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			ws, _ := st.Workers(context.Background(), time.Minute)
			if len(ws) > 0 && strings.HasPrefix(ws[len(ws)-1].ID, "med-") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("no heartbeat")
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		if err := wait(t, done); err != nil {
			t.Fatal(name, err)
		}
	}
}

func selfSigned(t *testing.T, dir string) (string, string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	kb, _ := x509.MarshalECPrivateKey(k)
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cert, key
}

func TestAPI(t *testing.T) {
	cert, key := selfSigned(t, t.TempDir())
	v, e := newEnv(t, map[string]string{"MED_TLS_CERT": cert, "MED_TLS_KEY": key, "MED_API_ADDR": "127.0.0.1:0", "MED_TRUST_PROXY": "1"})
	cancel, done := start(t, "med-api", e)
	<-v.ready
	pool := x509.NewCertPool()
	pem, _ := os.ReadFile(cert)
	pool.AppendCertsFromPEM(pem)
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	if code, body := get(t, c, "https://"+v.addr("api")+"/readyz"); code != 200 {
		t.Fatal(code, body)
	}
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	cert, key := selfSigned(t, dir)
	bad := map[string]map[string]string{
		"med-api":       {},
		"med-engine":    {"MED_WORKERS": "x"},
		"med-collector": {"MED_SCAN_EVERY": "-1s"},
	}
	for name, vars := range bad {
		vars["MED_METRICS_ADDR"] = "127.0.0.1:0"
		_, e := newEnv(t, vars)
		e.OnListen = nil
		if err := Run(context.Background(), name, e); err == nil {
			t.Fatal(name, "no error")
		}
	}
	for _, vars := range []map[string]string{
		{"MED_LEASE_TTL": "x"}, {"MED_POLL": "0s"}, {"MED_TICK_EVERY": "x"},
	} {
		vars["MED_METRICS_ADDR"] = "127.0.0.1:0"
		_, e := newEnv(t, vars)
		e.OnListen = nil
		if err := Run(context.Background(), "med-engine", e); err == nil {
			t.Fatal(vars, "no error")
		}
	}
	for _, vars := range []map[string]string{
		{"MED_TLS_CERT": cert, "MED_TLS_KEY": cert},
		{"MED_TLS_CERT": cert, "MED_TLS_KEY": key, "MED_SESSION_TTL": "x"},
		{"MED_TLS_CERT": cert, "MED_TLS_KEY": key, "MED_API_ADDR": "256.0.0.1:1"},
	} {
		_, e := newEnv(t, vars)
		if err := Run(context.Background(), "med-api", e); err == nil {
			t.Fatal(vars, "no error")
		}
	}
	_, e := newEnv(t, map[string]string{"MED_METRICS_ADDR": "256.0.0.1:1"})
	if err := Run(context.Background(), "med-collector", e); err == nil {
		t.Fatal("bad metrics address")
	}
	if err := Run(context.Background(), "med-nope", e); err == nil {
		t.Fatal("unknown component")
	}
	e.Open = func(context.Context) (*store.Store, error) { return nil, errors.New("down") }
	if err := Run(context.Background(), "med-collector", e); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatal(err)
	}
	if OSEnv().Getenv == nil {
		t.Fatal("OSEnv")
	}
}

func TestCollectorMetrics(t *testing.T) {
	reg := obs.NewRegistry()
	m := collectorMetrics{files: reg.Counter("a", "a", "stream"), errs: reg.Counter("b", "b", "stream")}
	m.Collected("S")
	m.ScanError("S")
	var b strings.Builder
	reg.Write(&b)
	if !strings.Contains(b.String(), `a{stream="S"} 1`) || !strings.Contains(b.String(), `b{stream="S"} 1`) {
		t.Fatal(b.String())
	}
}

func TestServeError(t *testing.T) {
	srv := &http.Server{}
	if err := serve(context.Background(), srv, func() error { return errors.New("boom") }); err == nil {
		t.Fatal("start error lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop := make(chan struct{})
	go func() { time.Sleep(50 * time.Millisecond); close(stop) }()
	if err := serve(ctx, srv, func() error { <-stop; return errors.New("after shutdown") }); err == nil {
		t.Fatal("serve error after shutdown lost")
	}
}

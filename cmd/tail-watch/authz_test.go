package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

func TestRoleFromCaps(t *testing.T) {
	t.Parallel()
	const capName = "example.com/cap/tail-watch"
	tests := []struct {
		name string
		caps tailcfg.PeerCapMap
		want role
	}{
		{"one viewer", capMap(capName, `{"role":"viewer"}`), roleViewer},
		{"one editor", capMap(capName, `{"role":"editor"}`), roleEditor},
		{"viewer plus editor", capMap(capName, `{"role":"viewer"}`, `{"role":"editor"}`), roleEditor},
		{"unknown role", capMap(capName, `{"role":"owner"}`), roleNone},
		{"non-object value", capMap(capName, `"viewer"`), roleNone},
		{"empty list", capMap(capName), roleNone},
		{"missing capability", capMap("example.com/cap/other", `{"role":"editor"}`), roleNone},
		{"nil map", nil, roleNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := roleFromCaps(tt.caps, capName); got != tt.want {
				t.Fatalf("roleFromCaps = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCallerAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		remote string
		xff    string
		want   string
	}{
		{"bare ipv4", "127.0.0.1:1", "100.64.0.7", "100.64.0.7"},
		{"bare ipv6", "[::1]:1", "fd7a:115c:a1e0::7", "fd7a:115c:a1e0::7"},
		{"ipv4 and port", "127.0.0.1:1", "100.64.0.7:443", "100.64.0.7:443"},
		{"bracketed ipv6 and port", "[::1]:1", "[fd7a:115c:a1e0::7]:443", "[fd7a:115c:a1e0::7]:443"},
		{"forwarded chain keeps the first", "127.0.0.1:1", "100.64.0.7, 10.0.0.1", "100.64.0.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
			r.RemoteAddr = tt.remote
			r.Header.Set("X-Forwarded-For", tt.xff)
			got, err := callerAddr(r)
			if err != nil || got != tt.want {
				t.Fatalf("callerAddr = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	user := whoisCaller("alice@example.com", "alice-phone", nil, `{"role":"viewer"}`)
	tagged := whoisCaller("", "db", []string{"tag:db"}, `{"role":"editor"}`)
	alice := wantCaller("alice@example.com", "alice-phone", nil, roleViewer)
	xff := map[string]string{"X-Forwarded-For": "100.64.0.7"}
	tests := []resolveCase{
		rc("user caller", "127.0.0.1:9", xff, ident(user, nil), "", alice, nil, 0),
		rc("tagged caller", "127.0.0.1:9", map[string]string{"X-Forwarded-For": "100.64.0.8"},
			ident(tagged, nil), "", wantCaller("", "db", []string{"tag:db"}, roleEditor), nil, 0),
		rc("forged capability header is ignored", "127.0.0.1:9", map[string]string{
			"X-Forwarded-For":            "100.64.0.7",
			"Tailscale-App-Capabilities": `{"example.com/cap/tail-watch":[{"role":"editor"}]}`,
		}, ident(user, nil), "", alice, nil, 0),
		rc("non-loopback connection", "100.64.0.9:5555", xff, ident(user, nil), "",
			wantCaller("", "", nil, roleNone), errNoIdentity, 0),
		rc("no header", "127.0.0.1:9", nil, ident(user, nil), "",
			wantCaller("", "", nil, roleNone), errNoIdentity, 0),
		rc("unknown address", "127.0.0.1:9", map[string]string{"X-Forwarded-For": "100.64.0.99"},
			ident(nil, local.ErrPeerNotFound), "", wantCaller("", "", nil, roleNone), errNoIdentity, 0),
		rc("lookup error", "127.0.0.1:9", xff, ident(nil, errors.New("tailscaled down")), "",
			wantCaller("", "", nil, roleNone), errNoIdentity, 0),
		rc("ipv6 address", "[::1]:9", map[string]string{"X-Forwarded-For": "fd7a:115c:a1e0::7"},
			ident(user, nil), "", alice, nil, 0),
		rc("service selects WhoIsForService", "127.0.0.1:9", xff, ident(user, nil), "svc:tail-watch", alice, nil, 1),
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checkResolveCase(t, tt)
		})
	}
}

func checkResolveCase(t *testing.T, tt resolveCase) {
	t.Helper()
	rs := roleResolver(tt.src, tt.service, true)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	r.RemoteAddr = tt.remote
	for k, v := range tt.headers {
		r.Header.Set(k, v)
	}
	got, err := rs.resolve(r)
	if tt.wantErr != nil {
		if !errors.Is(err, tt.wantErr) {
			t.Fatalf("resolve err = %v, want %v", err, tt.wantErr)
		}
		if len(tt.src.addrs) != 0 && tt.name == "non-loopback connection" {
			t.Fatal("WhoIs was called for a non-loopback connection")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	assertCaller(t, got, tt.want)
	if len(tt.src.svcs) != tt.wantSvc {
		t.Fatalf("WhoIsForService calls = %d, want %d", len(tt.src.svcs), tt.wantSvc)
	}
	if tt.wantSvc == 1 && tt.src.svcs[0] != tailcfg.ServiceName(tt.service) {
		t.Fatalf("service = %q", tt.src.svcs[0])
	}
}

func assertCaller(t *testing.T, got, want caller) {
	t.Helper()
	if got.User != want.User || got.Node != want.Node || got.Role != want.Role {
		t.Fatalf("caller = %+v, want %+v", got, want)
	}
	if !slices.Equal(got.Tags, want.Tags) {
		t.Fatalf("tags = %v, want %v", got.Tags, want.Tags)
	}
}

func TestDecide(t *testing.T) {
	t.Parallel()
	needs := []role{roleViewer, roleEditor}
	callers := []role{roleNone, roleViewer, roleEditor}
	for _, have := range callers {
		for _, need := range needs {
			got := decide(need, wantCaller("", "", nil, have))
			switch {
			case have.allows(need):
				if got != nil {
					t.Errorf("%s doing %s: %v", have, need, got)
				}
			case have == roleNone:
				if got == nil || got.Error() != "no role was granted" {
					t.Errorf("%s doing %s: %v", have, need, got)
				}
			default:
				want := "role " + string(need) + " is required"
				if got == nil || got.Error() != want {
					t.Errorf("%s doing %s: %v, want %q", have, need, got, want)
				}
			}
		}
	}
}

type fakeIdentity struct {
	who   *apitype.WhoIsResponse
	err   error
	addrs []string
	svcs  []tailcfg.ServiceName
}

func (f *fakeIdentity) WhoIs(_ context.Context, addr string) (*apitype.WhoIsResponse, error) {
	f.addrs = append(f.addrs, addr)
	return f.who, f.err
}

func (f *fakeIdentity) WhoIsForService(
	_ context.Context, addr string, svc tailcfg.ServiceName,
) (*apitype.WhoIsResponse, error) {
	f.addrs = append(f.addrs, addr)
	f.svcs = append(f.svcs, svc)
	return f.who, f.err
}

type resolveCase struct {
	name    string
	remote  string
	headers map[string]string
	src     *fakeIdentity
	service string
	want    caller
	wantErr error
	wantSvc int
}

func rc(
	name, remote string, headers map[string]string, src *fakeIdentity, service string,
	want caller, wantErr error, wantSvc int,
) resolveCase {
	return resolveCase{
		name: name, remote: remote, headers: headers, src: src, service: service,
		want: want, wantErr: wantErr, wantSvc: wantSvc,
	}
}

func ident(who *apitype.WhoIsResponse, err error) *fakeIdentity {
	return &fakeIdentity{who: who, err: err, addrs: nil, svcs: nil}
}

func wantCaller(user, node string, tags []string, rl role) caller {
	return caller{User: user, Node: node, Tags: tags, Role: rl}
}

func roleResolver(src identitySource, service string, enabled bool) *resolver {
	return &resolver{src: src, capability: "example.com/cap/tail-watch", service: service, enabled: enabled}
}

func capMap(name string, vals ...string) tailcfg.PeerCapMap {
	raws := make([]tailcfg.RawMessage, len(vals))
	for i, v := range vals {
		raws[i] = tailcfg.RawMessage(v)
	}
	out := make(tailcfg.PeerCapMap)
	out[tailcfg.PeerCapability(name)] = raws
	return out
}

func whoisCaller(login, node string, tags []string, grant string) *apitype.WhoIsResponse {
	const capName = "example.com/cap/tail-watch"
	return &apitype.WhoIsResponse{
		Node: &tailcfg.Node{Name: node + ".", ComputedName: node, Tags: tags},
		UserProfile: &tailcfg.UserProfile{
			ID: 0, LoginName: login, DisplayName: "", ProfilePicURL: "", Groups: nil,
		},
		CapMap: capMap(capName, grant),
	}
}

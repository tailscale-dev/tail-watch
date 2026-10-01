package main

// The QEMU process watching, qcow2 overlay, sftp file copy, bounded retry,
// probably-free port, and throwaway host-key handling in this file borrow
// patterns from Tailscale's VM harness at tailscale.com/tstest/integration/vms
// (BSD-3-Clause). That harness is all _test.go code and cannot be imported,
// so the ideas are copied and adapted here.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	vmCmdTimeout           = 2 * time.Minute
	vmRetryInterval        = 3 * time.Second
	vmSSHDeadline          = 6 * time.Minute
	vmConsoleTailBytes     = 16 * 1024
	vmImageDownloadTimeout = 15 * time.Minute
	vmSumsFetchTimeout     = 30 * time.Second
	ubuntuImageName        = "noble-server-cloudimg-amd64.img"
	ubuntuImageBaseURL     = "https://cloud-images.ubuntu.com/noble/current/"
)

// vm is one running QEMU guest, its serial console, and its SSH client.
type vm struct {
	t           *testing.T
	client      *ssh.Client
	sftp        *sftp.Client
	cmd         *exec.Cmd
	done        chan struct{}
	consolePath string
	secrets     []string
}

// redact strips auth keys (via redactSecrets) and every secret this run
// placed on the guest, such as the webhook signing secret.
func (v *vm) redact(s string) string {
	s = redactSecrets(s)
	for _, sec := range v.secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "redacted")
		}
	}
	return s
}

// Run executes one command over SSH with the default per-call timeout.
func (v *vm) Run(cmd string) (string, string, error) {
	return v.RunTimeout(vmCmdTimeout, cmd)
}

// RunTimeout executes one command in a fresh SSH session. On timeout the
// session is closed so the guest process is abandoned, not awaited.
func (v *vm) RunTimeout(timeout time.Duration, cmd string) (string, string, error) {
	if v.client == nil {
		return "", "", fmt.Errorf("no ssh client yet, cannot run %.60q", cmd)
	}
	sess, err := v.client.NewSession()
	if err != nil {
		return "", "", err
	}
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case runErr := <-done:
		_ = sess.Close()
		return stdout.String(), stderr.String(), runErr
	case <-time.After(timeout):
		_ = sess.Close()
		return "", "", fmt.Errorf("timed out after %s running %.80q", timeout, v.redact(cmd))
	case <-v.done:
		_ = sess.Close()
		return "", "", fmt.Errorf("qemu exited while running %.80q", v.redact(cmd))
	}
}

// MustRun runs cmd and fails the test with redacted output on error.
func (v *vm) MustRun(cmd string) string {
	v.t.Helper()
	stdout, stderr, err := v.Run(cmd)
	if err != nil {
		v.t.Fatalf("%.80q: %v\nstderr: %s", v.redact(cmd), err, v.redact(stderr))
	}
	return stdout
}

// Eventually retries fn until it succeeds or the deadline passes, then fails
// the test with the last error. Borrowed from the Tailscale harness's retry,
// stretched to convergence checks rather than fixed attempts.
func (v *vm) Eventually(desc string, timeout time.Duration, fn func() error) {
	v.t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for {
		if err = fn(); err == nil {
			return
		}
		if time.Now().Add(vmRetryInterval).After(deadline) {
			break
		}
		time.Sleep(vmRetryInterval)
	}
	v.t.Fatalf("%s: last error: %v", desc, err)
}

// Put copies a local file to the guest over sftp with the given mode and
// checks the byte count, like copyFile in Tailscale's vm_setup_test.go.
func (v *vm) Put(local, remote string, mode os.FileMode) {
	v.t.Helper()
	fin, err := os.Open(local)
	if err != nil {
		v.t.Fatal(err)
	}
	defer func() { _ = fin.Close() }()
	fi, err := fin.Stat()
	if err != nil {
		v.t.Fatal(err)
	}
	fout, err := v.sftp.Create(remote)
	if err != nil {
		v.t.Fatalf("create %s: %v", remote, err)
	}
	if cerr := fout.Chmod(mode); cerr != nil {
		_ = fout.Close()
		v.t.Fatalf("chmod %s: %v", remote, cerr)
	}
	n, err := io.Copy(fout, fin)
	if err != nil {
		_ = fout.Close()
		v.t.Fatalf("copy to %s: %v", remote, err)
	}
	if cerr := fout.Close(); cerr != nil {
		v.t.Fatalf("close %s: %v", remote, cerr)
	}
	if n != fi.Size() {
		v.t.Fatalf("copied %d bytes to %s, want %d", n, remote, fi.Size())
	}
}

// PutContent writes content to a local temp file and Puts it, so secrets can
// land on the guest in a 0600 file without appearing on a command line.
func (v *vm) PutContent(content []byte, remote string, mode os.FileMode) {
	v.t.Helper()
	local := filepath.Join(v.t.TempDir(), "put")
	if err := os.WriteFile(local, content, 0o600); err != nil {
		v.t.Fatal(err)
	}
	v.Put(local, remote, mode)
}

// Console returns the redacted tail of the serial console log.
func (v *vm) Console() string {
	raw, err := os.ReadFile(v.consolePath)
	if err != nil {
		return fmt.Sprintf("no console log: %v", err)
	}
	if len(raw) > vmConsoleTailBytes {
		raw = raw[len(raw)-vmConsoleTailBytes:]
	}
	return v.redact(string(raw))
}

// vmPreflight skips the test with a clear message when a host prerequisite is
// missing.
func vmPreflight(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"qemu-system-x86_64", "qemu-img", "pulumi"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is required for the VM systemd test: %v", bin, err)
		}
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("/dev/kvm is not accessible (join the kvm group): %v", err)
	}
	_ = f.Close()
}

// buildCollector builds the collector binary the way a release does, so the
// guest runs the same static binary the docs install.
func buildCollector(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "tail-watch")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	cmd.Dir = packageDir(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// vmSSHKey generates the throwaway ed25519 keypair cloud-init plants for root.
func vmSSHKey(t *testing.T) (string, ssh.Signer) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), signer
}

// ubuntuImage returns the cached Ubuntu 24.04 cloud image, downloading and
// verifying it against the published SHA256SUMS on cache miss or mismatch.
func ubuntuImage(t *testing.T) string {
	t.Helper()
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cacheDir, "tail-watch", "vmtest")
	if merr := os.MkdirAll(dir, 0o755); merr != nil {
		t.Fatal(merr)
	}
	want := ubuntuImageSum(t)
	path := filepath.Join(dir, ubuntuImageName)
	if got := fileSHA256(path); got == want {
		return path
	}
	url := ubuntuImageBaseURL + ubuntuImageName
	t.Logf("downloading %s to %s (first run only, ~600MB)", url, path)
	tmp := path + ".tmp"
	defer func() { _ = os.Remove(tmp) }()
	ctx, cancel := context.WithTimeout(t.Context(), vmImageDownloadTimeout)
	defer cancel()
	resp, err := vmHTTPGet(ctx, url)
	if err != nil {
		t.Fatalf("download %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", url, resp.Status)
	}
	fout, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, cerr := io.Copy(fout, resp.Body); cerr != nil {
		_ = fout.Close()
		t.Fatalf("download %s: %v", url, cerr)
	}
	if cerr := fout.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if got := fileSHA256(tmp); got != want {
		t.Fatalf("sha256 mismatch for %s: got %s, want %s", url, got, want)
	}
	if rerr := os.Rename(tmp, path); rerr != nil {
		t.Fatal(rerr)
	}
	return path
}

// ubuntuImageSum reads the image's expected sha256 from the published
// SHA256SUMS, fetched fresh so a stale "current" image is caught.
func ubuntuImageSum(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), vmSumsFetchTimeout)
	defer cancel()
	resp, err := vmHTTPGet(ctx, ubuntuImageBaseURL+"SHA256SUMS")
	if err != nil {
		t.Fatalf("fetch SHA256SUMS: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch SHA256SUMS: %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(raw)) {
		if strings.HasSuffix(strings.TrimSpace(line), "*"+ubuntuImageName) {
			return strings.Fields(line)[0]
		}
	}
	t.Fatalf("SHA256SUMS has no entry for %s", ubuntuImageName)
	return ""
}

func vmHTTPGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

func fileSHA256(path string) string {
	fin, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = fin.Close() }()
	h := sha256.New()
	if _, cerr := io.Copy(h, fin); cerr != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// mkLayeredQcow creates a per-run copy-on-write overlay over the cached base
// image, from mkLayeredQcow in Tailscale's vms_test.go.
func mkLayeredQcow(t *testing.T, base, dir string) string {
	t.Helper()
	qcow := filepath.Join(dir, "disk.qcow2")
	out, err := exec.CommandContext(t.Context(), "qemu-img", "create",
		"-f", "qcow2", "-b", base, "-F", "qcow2", qcow).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img create: %v\n%s", err, out)
	}
	return qcow
}

// probablyFreePort returns a port that was free a moment ago, the same trick
// as getProbablyFreePortNumber in the Tailscale harness.
func probablyFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if cerr := ln.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	return port
}

// launchVM boots the overlay in QEMU/KVM with user-mode networking: SSH is
// forwarded to 127.0.0.1:sshPort, and the smbios serial points cloud-init's
// nocloud-net datasource at the seed server on seedPort (slirp's 10.0.2.2 is
// the host). Process watching and cleanup follow mkVM in the Tailscale
// harness.
func launchVM(t *testing.T, dir, qcow string, sshPort, seedPort int) *vm {
	t.Helper()
	consolePath := filepath.Join(dir, "console.log")
	args := []string{
		"-machine", "q35,accel=kvm,usb=off,vmport=off,dump-guest-core=off",
		"-cpu", "host",
		"-m", "2048",
		"-smp", "2",
		"-drive", "file=" + qcow + ",if=virtio",
		"-netdev", fmt.Sprintf("user,id=n0,hostfwd=tcp:127.0.0.1:%d-:22", sshPort),
		"-device", "virtio-net-pci,netdev=n0",
		"-smbios", fmt.Sprintf("type=1,serial=ds=nocloud-net;s=http://10.0.2.2:%d/", seedPort),
		"-display", "none",
		"-serial", "file:" + consolePath,
	}
	t.Logf("qemu-system-x86_64 %s", strings.Join(args, " "))
	qemuLog, err := os.Create(filepath.Join(dir, "qemu.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "qemu-system-x86_64", args...)
	cmd.Stdout = qemuLog
	cmd.Stderr = qemuLog
	if serr := cmd.Start(); serr != nil {
		_ = qemuLog.Close()
		t.Fatal(serr)
	}
	v := &vm{
		t:           t,
		client:      nil,
		sftp:        nil,
		cmd:         cmd,
		done:        make(chan struct{}),
		consolePath: consolePath,
		secrets:     nil,
	}
	go func() {
		_ = cmd.Wait()
		close(v.done)
	}()
	t.Cleanup(func() {
		if kerr := v.cmd.Process.Kill(); kerr != nil {
			t.Logf("kill qemu: %v", kerr)
		}
		<-v.done
		_ = qemuLog.Close()
	})
	return v
}

// dialVM retries SSH until cloud-init has planted the key and sshd is up, or
// the deadline passes. InsecureIgnoreHostKey is acceptable for a throwaway
// localhost VM, as in the Tailscale harness's setupSSHShell.
func dialVM(t *testing.T, v *vm, sshPort int, signer ssh.Signer) {
	t.Helper()
	var cfg ssh.ClientConfig
	cfg.User = "root"
	cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	cfg.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	cfg.Timeout = 5 * time.Second
	deadline := time.Now().Add(vmSSHDeadline)
	var err error
	for time.Now().Before(deadline) {
		select {
		case <-v.done:
			t.Fatalf("qemu exited before ssh came up\nconsole:\n%s", v.Console())
		default:
		}
		var client *ssh.Client
		client, err = ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(sshPort)), &cfg)
		if err == nil {
			sftpClient, serr := sftp.NewClient(client)
			if serr != nil {
				_ = client.Close()
				err = serr
			} else {
				v.client = client
				v.sftp = sftpClient
				t.Cleanup(func() {
					_ = sftpClient.Close()
					_ = client.Close()
				})
				return
			}
		}
		time.Sleep(vmRetryInterval)
	}
	t.Fatalf("ssh to the VM did not come up: %v\nconsole:\n%s", err, v.Console())
}

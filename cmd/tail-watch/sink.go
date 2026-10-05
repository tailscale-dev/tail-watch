package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout  = 10 * time.Second
	maxResponseRead = 64 << 10
	signatureHeader = "Tail-Watch-Signature"

	headerContentType = "Content-Type"
	headerUserAgent   = "User-Agent"
)

// newSinkWithEnv builds the delivery code for one configured sink. env, when
// set, carries every connection so sinks follow the host routing table.
func newSinkWithEnv(sc SinkConfig, col CollectorInfo, env netEnv) (sink, error) {
	client := newSinkClient(env)
	switch sc.Type {
	case sinkWebhook:
		return newWebhookSink(sc, client), nil
	case sinkSlack:
		return newSlackSink(sc, col, client), nil
	case sinkTeams:
		return newTeamsSink(sc, col, client), nil
	default:
		return nil, fmt.Errorf("sink %s: unknown type %q", sc.Name, sc.Type)
	}
}

func newSinkClient(env netEnv) *http.Client {
	tr := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
	}
	if env != nil {
		tr.DialContext = env.Dial
	}
	return &http.Client{
		Transport: tr,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// marshalJSON encodes v without HTML escaping, so payloads stay readable and
// the exact bytes are what gets signed.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// signBody computes the Tail-Watch-Signature value for body sent at unix time
// t: lowercase hex HMAC-SHA256 over "<t>.<body>".
func signBody(secret string, t int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(t, 10) + "."))
	mac.Write(body)
	return "t=" + strconv.FormatInt(t, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// classifyStatus maps an HTTP response to a delivery result.
func classifyStatus(status int, h http.Header, now time.Time) deliveryResult {
	res := deliveryResult{Class: resultOK, Status: status, Reason: "", RetryAfter: 0, Err: nil}
	switch {
	case status >= 200 && status < 300:
		return res
	case status == http.StatusRequestTimeout:
		res.Class, res.Reason = resultRetry, failTimeout
	case status == http.StatusTooManyRequests:
		res.Class, res.Reason = resultRetry, failHTTP429
		res.RetryAfter = parseRetryAfter(h.Get("Retry-After"), now)
	case status >= http.StatusInternalServerError:
		res.Class, res.Reason = resultRetry, failHTTP5xx
	default:
		res.Class, res.Reason = resultPermanent, failHTTP4xx
	}
	return res
}

// parseRetryAfter accepts delay seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(secs, 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}

// classifyError maps a transport error to a delivery result, with the URL
// reduced to scheme and host.
func classifyError(err error, rawURL string) deliveryResult {
	res := deliveryResult{
		Class: resultRetry, Status: 0, Reason: failNetwork, RetryAfter: 0,
		Err: errors.New(strings.ReplaceAll(err.Error(), rawURL, redactURL(rawURL))),
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		res.Reason = failTimeout
	}
	return res
}

// httpPost is one POST shared by the webhook and Slack sinks. The URL is read
// at send time so a rotated secret needs no restart.
func httpPost(
	ctx context.Context,
	client *http.Client,
	url string,
	body []byte,
	headers map[string]string,
	now time.Time,
) deliveryResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return deliveryResult{
			Class: resultPermanent, Status: 0, Reason: failHTTP4xx, RetryAfter: 0,
			Err: errors.New("invalid sink URL " + redactURL(url)),
		}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return classifyError(err, url)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseRead))
	return classifyStatus(resp.StatusCode, resp.Header, now)
}

func secretUnavailable(err error) deliveryResult {
	return deliveryResult{Class: resultRetry, Status: 0, Reason: failNetwork, RetryAfter: 0, Err: err}
}

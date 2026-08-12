package pipeline

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// roundTripperFunc lets a test stand in for the network.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func journalIn(t *testing.T) (*Journal, string) {
	t.Helper()
	dir := t.TempDir()
	j, err := OpenJournal(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	return j, filepath.Join(dir, "log", JournalFile)
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	return string(body)
}

// The whole exchange has to survive, and the request has to reach the network
// unchanged: reading a body to record it consumes it, and forgetting to put it
// back would send an empty request that still looked right in the log.
func TestJournalRecordsTheExchangeAndStillSendsIt(t *testing.T) {
	journal, path := journalIn(t)

	var delivered string
	client := &http.Client{Transport: journal.Transport(roundTripperFunc(
		func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			delivered = string(body)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"hi"}}]}`)),
				Header:     http.Header{},
			}, nil
		}))}

	req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"the article"}]}`))
	req.Header.Set("Authorization", "Bearer sk-secret-value")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if delivered != `{"model":"m","messages":[{"role":"user","content":"the article"}]}` {
		t.Errorf("the request reached the network as %q — recording it consumed the body", delivered)
	}
	if string(got) != `{"choices":[{"message":{"content":"hi"}}]}` {
		t.Errorf("the caller read %q; the response body was not restored", got)
	}

	journalled := read(t, path)
	for _, want := range []string{
		"api.example/v1/chat/completions",
		"the article",     // the request
		`"content": "hi"`, // and the reply, pretty-printed
		"response 200",
	} {
		if !strings.Contains(journalled, want) {
			t.Errorf("the journal is missing %q\n---\n%s", want, journalled)
		}
	}
}

// The journal is a file people attach to bug reports. It must never carry the
// key, which is why headers are not recorded at all rather than filtered.
func TestJournalNeverRecordsCredentials(t *testing.T) {
	journal, path := journalIn(t)

	client := &http.Client{Transport: journal.Transport(roundTripperFunc(
		func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
		}))}

	req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer sk-proj-must-not-appear")
	req.Header.Set("X-Api-Key", "sk-ant-must-not-appear")
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}

	journalled := read(t, path)
	for _, secret := range []string{"sk-proj-must-not-appear", "sk-ant-must-not-appear", "Authorization", "X-Api-Key"} {
		if strings.Contains(journalled, secret) {
			t.Errorf("the journal contains %q", secret)
		}
	}
}

// A call that never arrived is the most interesting kind to have a record of.
func TestJournalRecordsAFailedCall(t *testing.T) {
	journal, path := journalIn(t)

	client := &http.Client{Transport: journal.Transport(roundTripperFunc(
		func(*http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		}))}

	req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/messages", strings.NewReader("{}"))
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected the error to reach the caller")
	}
	if journalled := read(t, path); !strings.Contains(journalled, "failed after") {
		t.Errorf("a failed call left no record:\n%s", journalled)
	}
}

// Four analyses run at once. Two exchanges interleaved inside the file would
// produce a record that reads as neither.
func TestJournalIsSafeUnderConcurrency(t *testing.T) {
	journal, path := journalIn(t)

	client := &http.Client{Transport: journal.Transport(roundTripperFunc(
		func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: http.Header{}}, nil
		}))}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/messages",
				strings.NewReader(`{"n":"payload"}`))
			resp, err := client.Do(req)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	journalled := read(t, path)
	if got := strings.Count(journalled, "===== "); got != 20 {
		t.Errorf("got %d entries, want 20", got)
	}
	if got := strings.Count(journalled, "----- request"); got != 20 {
		t.Errorf("got %d requests, want 20", got)
	}
}

// Off is the default, and off must cost nothing: a nil journal hands back the
// transport it was given rather than wrapping it.
func TestNilJournalDoesNotWrap(t *testing.T) {
	var journal *Journal
	base := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	if got := journal.Transport(base); &got == nil {
		t.Fatal("unreachable")
	}
	if _, wrapped := journal.Transport(base).(*journalTransport); wrapped {
		t.Error("a disabled journal still wrapped the transport")
	}
	if err := journal.Close(); err != nil {
		t.Errorf("closing a disabled journal returned %v", err)
	}
}

// Restarting must not discard the session somebody turned this on to capture.
func TestJournalAppendsAcrossOpens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "log")

	for i := 0; i < 2; i++ {
		j, err := OpenJournal(dir)
		if err != nil {
			t.Fatalf("OpenJournal: %v", err)
		}
		client := &http.Client{Transport: j.Transport(roundTripperFunc(
			func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
			}))}
		req, _ := http.NewRequest(http.MethodPost, "https://api.example/x", strings.NewReader("{}"))
		if _, err := client.Do(req); err != nil {
			t.Fatalf("Do: %v", err)
		}
		j.Close()
	}

	if got := strings.Count(read(t, filepath.Join(dir, JournalFile)), "===== "); got != 2 {
		t.Errorf("got %d entries after two runs, want 2: the second open truncated the file", got)
	}
}

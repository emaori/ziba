package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// JournalFile is the name of the record, inside whichever directory is
// configured. Fixed rather than settable: one fewer thing to get wrong when
// asking somebody where their log went.
const JournalFile = "modelJournal.txt"

// Journal writes every request made to a model, and what came back.
//
// It exists for the question that cannot be answered from the outside: not
// "what did the model say" — that is in the database — but "what exactly was it
// asked". A prompt is assembled from the interests file, the article, a schema
// and a handful of rules about declared categories, and when the answer is
// surprising the assembled result is the thing to read. Reconstructing it by
// eye from four source files is how the wrong conclusion gets reached.
//
// It records at the transport, below both providers, which is deliberate. A
// journal written at each call site is a journal that a later call site forgets
// to write to; this one cannot be bypassed by adding a stage, because every
// request either goes through this round tripper or does not reach the network.
//
// Off unless asked for. It holds the full text of every article sent, so it
// grows at roughly the size of everything collected and is not something to
// leave running.
type Journal struct {
	mu   sync.Mutex
	file *os.File
}

// OpenJournal creates the directory if it is missing and opens the record for
// appending.
//
// Appending, so restarting the container does not discard the session that
// prompted somebody to turn this on. Unbuffered, because the reason to read it
// is usually that something went wrong, and a buffer is exactly what would be
// lost when it does.
func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("model journal: create the log directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, JournalFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// The message names the likely cause because there is really only one.
		// A bind-mounted directory keeps the host's ownership, the container
		// runs as nobody, and a directory created by root on the host is
		// therefore unwritable — on Linux. Docker Desktop on macOS remaps
		// ownership and hides this entirely, so it is a failure that appears
		// only on the machine it matters on.
		return nil, fmt.Errorf("model journal: open %s: %w\n"+
			"if this is a bind-mounted directory, the container runs as uid 65534: "+
			"chown 65534:65534 the directory on the host, or unset ZIBA_MODEL_JOURNAL", path, err)
	}
	return &Journal{file: file}, nil
}

// Close releases the file.
func (j *Journal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.file.Close()
}

// Path is where the record is being written, for a startup log line that saves
// somebody looking.
func (j *Journal) Path() string {
	if j == nil {
		return ""
	}
	return j.file.Name()
}

// Transport wraps a round tripper so that everything passing through it is
// recorded. A nil Journal returns the original, so the caller does not have to
// branch on whether the option is on.
func (j *Journal) Transport(base http.RoundTripper) http.RoundTripper {
	if j == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &journalTransport{journal: j, base: base}
}

type journalTransport struct {
	journal *Journal
	base    http.RoundTripper
}

func (t *journalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The body has to be read to be recorded, and read again to be sent, so it
	// is replaced with a fresh reader over the same bytes.
	var sent []byte
	if req.Body != nil {
		var err error
		if sent, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(sent))
	}

	started := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(started)

	if err != nil {
		// A call that never arrived is the most interesting kind to have a
		// record of, so the failure is written rather than swallowed.
		t.journal.write(req, sent, nil, 0, elapsed, err)
		return nil, err
	}

	received, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(received))

	t.journal.write(req, sent, received, resp.StatusCode, elapsed, nil)
	return resp, nil
}

// write appends one exchange.
//
// Headers are not recorded, and that is the point rather than an omission: the
// API key is a header, and a debugging aid that writes credentials into a file
// people attach to bug reports is a worse problem than the one it solves. The
// address and the body are what is being debugged.
func (j *Journal) write(req *http.Request, sent, received []byte, status int, elapsed time.Duration, callErr error) {
	var b bytes.Buffer

	fmt.Fprintf(&b, "\n===== %s  %s %s\n",
		time.Now().Format(time.RFC3339), req.Method, req.URL)
	fmt.Fprintf(&b, "----- request\n%s\n", indented(sent))

	switch {
	case callErr != nil:
		fmt.Fprintf(&b, "----- failed after %s\n%v\n", elapsed.Round(time.Millisecond), callErr)
	default:
		fmt.Fprintf(&b, "----- response %d after %s\n%s\n",
			status, elapsed.Round(time.Millisecond), indented(received))
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	// Four analyses run at once, so one write per exchange under one lock: two
	// interleaved requests would otherwise produce a file that reads as neither.
	//
	// A failed write is not worth failing an analysis over — the article matters
	// and the debugging aid does not — but it must not be silent either, or the
	// missing entry looks like a request that was never made.
	if _, err := j.file.Write(b.Bytes()); err != nil {
		fmt.Fprintf(os.Stderr, "model journal: %v\n", err)
	}
}

// indented pretty-prints JSON, and passes anything else through untouched: an
// error page from a proxy is worth having in the record exactly as it arrived.
func indented(body []byte) string {
	if len(body) == 0 {
		return "(empty)"
	}
	var out bytes.Buffer
	if err := json.Indent(&out, body, "", "  "); err != nil {
		return string(body)
	}
	return out.String()
}

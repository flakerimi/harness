package provider

import (
	"bufio"
	"io"
	"strings"
)

// parseSSE reads a Server-Sent Events stream and invokes onData with the
// payload of each "data:" line. "[DONE]" sentinels and blank payloads are
// skipped. It uses bufio.Reader (not Scanner) so arbitrarily long lines —
// large streamed tool-input JSON, for instance — never overflow a token cap.
func parseSSE(r io.Reader, onData func(data string) error) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				data := strings.TrimSpace(after)
				if data != "" && data != "[DONE]" {
					if cbErr := onData(data); cbErr != nil {
						return cbErr
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

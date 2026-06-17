package tool

import "testing"

func TestParseDDG(t *testing.T) {
	html := `
	<a class="result__a" href="/l/?kh=-1&uddg=https%3A%2F%2Fexample.com%2Fjane&rut=abc">Jane <b>Doe</b></a>
	<a class="result__snippet" href="x">CEO &amp; founder at Example Corp</a>
	<a class="result__a" href="//direct.example.org/page">Second Result</a>
	<a class="result__snippet" href="x">A second snippet</a>
	`
	res := parseDDG(html, 10)
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}

	if res[0].URL != "https://example.com/jane" {
		t.Errorf("result 0 url = %q, want decoded https://example.com/jane", res[0].URL)
	}
	if res[0].Title != "Jane Doe" {
		t.Errorf("result 0 title = %q, want \"Jane Doe\" (tags stripped)", res[0].Title)
	}
	if res[0].Snippet != "CEO & founder at Example Corp" {
		t.Errorf("result 0 snippet = %q, want entities unescaped", res[0].Snippet)
	}

	if res[1].URL != "https://direct.example.org/page" {
		t.Errorf("result 1 url = %q, want protocol-relative made https", res[1].URL)
	}
}

func TestParseDDGRespectsLimit(t *testing.T) {
	html := `<a class="result__a" href="a">A</a><a class="result__a" href="b">B</a><a class="result__a" href="c">C</a>`
	if got := len(parseDDG(html, 2)); got != 2 {
		t.Errorf("limit not respected: got %d, want 2", got)
	}
}

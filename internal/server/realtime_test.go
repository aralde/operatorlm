package server

import (
	"reflect"
	"strings"
	"testing"
)

func collectSentences(deltas []string) []string {
	var out []string
	sp := &sentenceSplitter{emit: func(s string) { out = append(out, s) }}
	for _, d := range deltas {
		sp.Feed(d)
	}
	sp.Close()
	return out
}

func TestSentenceSplitterBasic(t *testing.T) {
	got := collectSentences([]string{"Hola. ", "¿Todo bien? Sí, ", "todo bien."})
	want := []string{"Hola.", "¿Todo bien?", "Sí, todo bien."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSentenceSplitterDecimalsStayWhole(t *testing.T) {
	got := collectSentences([]string{"El valor de pi es 3.14159 aproximadamente. Fin."})
	want := []string{"El valor de pi es 3.14159 aproximadamente.", "Fin."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSentenceSplitterTokenBoundaries(t *testing.T) {
	// Sentence end split across deltas: "…listo." then " Ahora"
	got := collectSentences([]string{"Todo ", "listo", ".", " Ahora sí."})
	want := []string{"Todo listo.", "Ahora sí."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSentenceSplitterNewlines(t *testing.T) {
	got := collectSentences([]string{"Primera línea\nSegunda línea\n"})
	want := []string{"Primera línea", "Segunda línea"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSentenceSplitterForceFlushLongRuns(t *testing.T) {
	long := strings.Repeat("palabra ", 80) // > splitterMaxBytes, no punctuation
	got := collectSentences([]string{long})
	if len(got) < 2 {
		t.Fatalf("expected forced flushes on a punctuation-free run, got %d chunks", len(got))
	}
}

func TestSentenceSplitterCloseFlushesRemainder(t *testing.T) {
	got := collectSentences([]string{"sin puntuacion final"})
	want := []string{"sin puntuacion final"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSSETextCollectorParsesDeltas(t *testing.T) {
	var got strings.Builder
	c := &sseTextCollector{onDelta: func(d string) { got.WriteString(d) }}
	c.WriteHeader(200)
	// Deltas split across Write calls, including a partial line boundary.
	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hola\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"cont",
		"ent\":\" mundo\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	for _, ch := range chunks {
		if _, err := c.Write([]byte(ch)); err != nil {
			t.Fatal(err)
		}
	}
	if got.String() != "Hola mundo" {
		t.Fatalf("got %q, want %q", got.String(), "Hola mundo")
	}
}

func TestSSETextCollectorCapturesErrors(t *testing.T) {
	c := &sseTextCollector{onDelta: func(string) { t.Fatal("no deltas expected on error") }}
	c.WriteHeader(502)
	_, _ = c.Write([]byte("upstream exploded"))
	if c.status != 502 || c.errBody.String() != "upstream exploded" {
		t.Fatalf("status=%d body=%q", c.status, c.errBody.String())
	}
}

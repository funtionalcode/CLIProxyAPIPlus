package geminiweb

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func testFrame(text string, complete bool) string {
	candidate := make([]any, 9)
	candidate[0], candidate[1] = "rc_test", []string{text}
	if complete {
		candidate[8] = []int{2}
	} else {
		candidate[8] = []int{1}
	}
	inner, _ := json.Marshal([]any{nil, []string{"c_test", "r_test"}, nil, nil, []any{candidate}})
	frame, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(inner)}})
	return fmt.Sprintf("%d\n%s\n", len(frame), frame)
}

func TestReadStreamCumulativeUnicodeAndCompletion(t *testing.T) {
	stream := ")]}'\n\n" + testFrame("你", false) + testFrame("你好", false) + testFrame("你好", true)
	var events []string
	got, err := ReadStream(strings.NewReader(stream), func(s string) error { events = append(events, s); return nil })
	if err != nil || got != "你好" || len(events) != 2 {
		t.Fatalf("answer=%q events=%v err=%v", got, events, err)
	}
}

func TestReadStreamRejectsIncompleteAndChangedAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"empty": ")]}'\n\n", "truncated": testFrame("partial", false),
		"rewritten": testFrame("first", false) + testFrame("other", true),
		"malformed": "<html>Login required</html>", "rejected": "[[\"wrb.fr\",null,null,null,null,[1037]]]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadStream(strings.NewReader(body), nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPromptPreservesTextRolesAndRejectsUnsupportedInput(t *testing.T) {
	got, err := Prompt([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	if err != nil || got != "hello" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	got, err = Prompt([]byte(`{"messages":[{"role":"system","content":"system rule"},{"role":"user","content":"question"},{"role":"assistant","content":"answer"},{"role":"user","content":"follow up"}]}`))
	if err != nil || !strings.Contains(got, "[system]\nsystem rule") || !strings.Contains(got, "[assistant]\nanswer") || !strings.Contains(got, "follow up") {
		t.Fatalf("got=%q err=%v", got, err)
	}
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}]}`,
		`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function"}]}`,
		`{"messages":[{"role":"tool","content":"tool result"}]}`,
		`{"messages":[]}`, `{"messages":[{"role":"user","content":""}]}`,
	} {
		if _, err := Prompt([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestValidateInputBeforeLossyProtocolTranslation(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"function_call_output","output":"ok"}]}`,
		`{"input":"continue","previous_response_id":"resp_test"}`,
		`{"input":"hello","text":{"format":{"type":"json_schema"}}}`,
		`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"abc"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"document","source":{}}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,abc"}]}]}`,
	} {
		if err := ValidateInput([]byte(body)); err == nil {
			t.Fatalf("accepted unsupported input: %s", body)
		}
	}
	if err := ValidateInput([]byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestPayloadSelectsObservedWebMode(t *testing.T) {
	for _, model := range Models() {
		var body []any
		if err := json.Unmarshal(Payload("hello", model.Mode), &body); err != nil {
			t.Fatal(err)
		}
		if body[79] != float64(model.Mode) {
			t.Fatalf("wrong mode for %s", model.ID)
		}
		if body[2] != nil || body[71] != nil {
			t.Fatal("new request reused a conversation")
		}
	}
}

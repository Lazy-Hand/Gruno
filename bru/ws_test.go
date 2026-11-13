package bru

import (
	"strings"
	"testing"
)

const wsSample = `meta {
  name: web-socket
  type: ws
  seq: 1
  tags: [
    smoke
    ws
  ]
}

ws {
  url: wss://echo.websocket.org
}

body:ws {
  message 1 [json] {
    {"name":"Bruno"}
  }

  message 2 [json] {
    {"msg":"hi"}
  }
}
`

func TestWebSocketRoundTrip(t *testing.T) {
	r, err := ParseRequest(wsSample)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsWebSocket() || r.URL != "wss://echo.websocket.org" {
		t.Fatalf("request: %+v", r)
	}
	if len(r.Meta.Tags) != 2 || r.Meta.Tags[0] != "smoke" {
		t.Fatalf("tags: %+v", r.Meta.Tags)
	}
	if len(r.WSMessages) != 2 || r.WSMessages[0].Name != "message 1" ||
		r.WSMessages[0].Kind != "json" || r.WSMessages[0].Body != `{"name":"Bruno"}` {
		t.Fatalf("messages: %+v", r.WSMessages)
	}
	out := SerializeRequest(r)
	if strings.Contains(out, "\nget {") || strings.Contains(out, "body:json") {
		t.Fatalf("websocket serialized as http: %s", out)
	}
	r2, err := ParseRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.WSMessages) != 2 || r2.WSMessages[1].Body != `{"msg":"hi"}` ||
		len(r2.Meta.Tags) != 2 || r2.URL != r.URL {
		t.Fatalf("round trip: %+v\n%s", r2, out)
	}
}

func TestCanonicalBodyBlockIsJSON(t *testing.T) {
	src := "meta {\n  name: B\n  type: http\n}\n\npost {\n  url: https://x\n  body: json\n  auth: none\n}\n\nbody {\n  {\"a\": 1}\n}\n"
	r, err := ParseRequest(src)
	if err != nil {
		t.Fatal(err)
	}
	if r.Body != "json" || r.BodyJSON != `{"a": 1}` {
		t.Fatalf("body: %q %q", r.Body, r.BodyJSON)
	}
}

func TestWebSocketKeepsHeaders(t *testing.T) {
	src := `meta {
  name: ws
  type: ws
  seq: 1
}

ws {
  url: wss://x
}

headers {
  Authorization: Bearer t
}

body:ws {
  hi [text] {
    hello
  }
}
`
	r, err := ParseRequest(src)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Headers.Get("Authorization"); v != "Bearer t" {
		t.Fatalf("headers: %+v", r.Headers)
	}
	out := SerializeRequest(r)
	if !strings.Contains(out, "Authorization: Bearer t") {
		t.Fatalf("header dropped on save:\n%s", out)
	}
}

// A pretty-printed message body has braces of its own; they must not end
// the message block.
func TestWebSocketPrettyBodyBraces(t *testing.T) {
	src := `meta {
  name: ws
  type: ws
}

ws {
  url: wss://x
}

body:ws {
  pretty [json] {
    {
      "a": 1,
      "b": {
        "c": 2
      }
    }
  }
}
`
	r, err := ParseRequest(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.WSMessages) != 1 {
		t.Fatalf("messages: %+v", r.WSMessages)
	}
	want := "{\n  \"a\": 1,\n  \"b\": {\n    \"c\": 2\n  }\n}"
	if got := r.WSMessages[0].Body; got != want {
		t.Fatalf("body:\n%q\nwant:\n%q", got, want)
	}
	r2, err := ParseRequest(SerializeRequest(r))
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.WSMessages) != 1 || r2.WSMessages[0].Body != want {
		t.Fatalf("round trip: %+v", r2.WSMessages)
	}
}

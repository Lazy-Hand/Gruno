package send

// Data contracts shared by the send path: the editable request view,
// response head/result and the pair tables.

// Pair is a key/value row used by Query, Headers, form fields, Path
// params and variables. Arrays keep duplicate keys and order; never
// map-ify.
type Pair struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// BodyMode selects how a request body is encoded.
type BodyMode string

const (
	BodyNone       BodyMode = "none"
	BodyJSON       BodyMode = "json"
	BodyText       BodyMode = "text"
	BodyURLEncoded BodyMode = "urlencoded"
)

// BodySpec is the request body editor state. Switching modes keeps the
// other fields' content.
type BodySpec struct {
	Mode BodyMode `json:"mode"`
	Text string   `json:"text"`
	Form []Pair   `json:"form"`
}

// AuthMode selects the authentication helper.
type AuthMode string

const (
	AuthNone    AuthMode = "none"
	AuthBasic   AuthMode = "basic"
	AuthBearer  AuthMode = "bearer"
	AuthAPIKey  AuthMode = "apikey"
	AuthDigest  AuthMode = "digest"
	AuthInherit AuthMode = "inherit"
	AuthOAuth2  AuthMode = "oauth2"
)

// AuthLocation is where an API key is attached.
type AuthLocation string

const (
	AuthLocationHeader AuthLocation = "header"
	AuthLocationQuery  AuthLocation = "query"
)

// AuthSpec is the authentication editor state. Basic/Bearer use the
// Authorization header; APIKey uses Key at Location.
type AuthSpec struct {
	Mode     AuthMode     `json:"mode"`
	Username string       `json:"username"`
	Password string       `json:"password"`
	Token    string       `json:"token"`
	Key      string       `json:"key"`
	Value    string       `json:"value"`
	Location AuthLocation `json:"location"`
	OAuth2   *OAuth2Spec  `json:"oauth2,omitempty"`
}

// RequestData is the editable request: method, URL template and the
// pair tables. It is the send-time view of a bru.Request.
type RequestData struct {
	Name       string   `json:"name"`
	Method     string   `json:"method"`
	URL        string   `json:"url"`
	Query      []Pair   `json:"query"`
	PathParams []Pair   `json:"pathParams"`
	Headers    []Pair   `json:"headers"`
	Body       BodySpec `json:"body"`
	Auth       AuthSpec `json:"auth"`
}

// SendInput is the payload of a send: the request plus the variable
// scopes to resolve against, ordered lowest precedence first.
type SendInput struct {
	Request  RequestData `json:"request"`
	Scopes   [][]Pair    `json:"scopes"`
	Settings Settings    `json:"settings"`
}

// ResponseHead arrives on the send channel as soon as response headers are
// received, before the body is read.
type ResponseHead struct {
	StatusCode    int    `json:"statusCode"`
	StatusText    string `json:"statusText"`
	URL           string `json:"url"`
	Protocol      string `json:"protocol"`
	Headers       []Pair `json:"headers"`
	ContentLength int64  `json:"contentLength"`
	// Sent is the request as it went out: variables resolved, auth
	// attached (the first hop; URL above is where redirects ended).
	Sent SentRequest `json:"sent"`
}

// SentRequest is the wire view of a request for the timeline.
type SentRequest struct {
	Method  string `json:"method"`
	URL     string `json:"url"`
	Headers []Pair `json:"headers"`
}

// BodyKind classifies the response body for display.
type BodyKind string

const (
	BodyKindText               BodyKind = "text"
	BodyKindBinary             BodyKind = "binary"
	BodyKindUnsupportedCharset BodyKind = "unsupported-charset"
)

// ErrorCode classifies a failed send. Empty means the request completed;
// HTTP 4xx/5xx are still successful sends.
type ErrorCode string

const (
	ErrNone       ErrorCode = ""
	ErrInvalidReq ErrorCode = "invalid-request"
	ErrCanceled   ErrorCode = "canceled"
	ErrTimeout    ErrorCode = "timeout"
	ErrNetwork    ErrorCode = "network"
	ErrBodyRead   ErrorCode = "body-read"
	ErrScript     ErrorCode = "script"
	ErrAuth       ErrorCode = "auth"
)

// ResponseResult is the final outcome of a send, body included.
type ResponseResult struct {
	BodyKind     BodyKind  `json:"bodyKind"`
	BodyText     string    `json:"bodyText"`
	SizeBytes    int64     `json:"sizeBytes"`
	Truncated    bool      `json:"truncated"`
	DurationMs   float64   `json:"durationMs"`
	ErrorCode    ErrorCode `json:"errorCode"`
	ErrorMessage string    `json:"errorMessage"`
	// Notes are send steps worth showing in the timeline, such as
	// where an OAuth2 token came from.
	Notes []string `json:"notes,omitempty"`
}

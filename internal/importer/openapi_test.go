package importer

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gruno/bru"
)

func loadPetstore(t *testing.T) *Collection {
	t.Helper()
	data, err := os.ReadFile("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseOpenAPI(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func findReq(reqs []*bru.Request, name string) *bru.Request {
	for _, r := range reqs {
		if r.Meta.Name == name {
			return r
		}
	}
	return nil
}

func TestParseOpenAPIStructure(t *testing.T) {
	c := loadPetstore(t)
	if c.Name != "Petstore" {
		t.Fatalf("name %q", c.Name)
	}
	if v, _ := c.Bru.Vars.Get("baseUrl"); v != "https://petstore.test/v2" {
		t.Fatalf("baseUrl %q", v)
	}
	if c.Bru.Auth != "apikey" {
		t.Fatalf("collection auth %q", c.Bru.Auth)
	}
	if v, _ := c.Bru.AuthAPIKey.Get("placement"); v != "queryparams" {
		t.Fatalf("placement %q", v)
	}
	if len(c.Folders) != 1 || c.Folders[0].Name != "Pets" {
		t.Fatalf("folders %+v", c.Folders)
	}
	pets := c.Folders[0]
	if len(pets.Requests) != 3 {
		t.Fatalf("Pets requests %d", len(pets.Requests))
	}
	// Untagged operations sit at the root.
	if len(c.Requests) != 3 {
		t.Fatalf("root requests %d", len(c.Requests))
	}
}

func TestParseOpenAPIOperations(t *testing.T) {
	c := loadPetstore(t)
	list := findReq(c.Folders[0].Requests, "List pets")
	if list == nil || list.Method != "get" || list.URL != "{{baseUrl}}/pets" {
		t.Fatalf("list %+v", list)
	}
	if len(list.ParamsQuery) != 2 {
		t.Fatalf("query %+v", list.ParamsQuery)
	}
	if list.ParamsQuery[0].Key != "limit" || list.ParamsQuery[0].Value != "20" || list.ParamsQuery[0].Enabled {
		t.Fatalf("optional param %+v", list.ParamsQuery[0])
	}
	if list.ParamsQuery[1].Key != "status" || list.ParamsQuery[1].Value != "available" || !list.ParamsQuery[1].Enabled {
		t.Fatalf("required param %+v", list.ParamsQuery[1])
	}
	// Path-level X-Trace header is inherited, required, enabled.
	if len(list.Headers) != 1 || list.Headers[0].Key != "X-Trace" || list.Headers[0].Value != "abc" || !list.Headers[0].Enabled {
		t.Fatalf("headers %+v", list.Headers)
	}
	if list.Auth != "inherit" {
		t.Fatalf("auth %q", list.Auth)
	}

	create := findReq(c.Folders[0].Requests, "createPet")
	if create == nil || create.Method != "post" || create.Body != "json" || create.Auth != "none" {
		t.Fatalf("create %+v", create)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(create.BodyJSON), &body); err != nil {
		t.Fatalf("body %q: %v", create.BodyJSON, err)
	}
	if body["name"] != "Rex" || len(body["tags"].([]any)) != 1 {
		t.Fatalf("body %+v", body)
	}

	get := findReq(c.Folders[0].Requests, "Get a pet")
	if get == nil || get.URL != "{{baseUrl}}/pets/:petId" || get.Auth != "bearer" {
		t.Fatalf("get %+v", get)
	}
	if v, _ := get.AuthBearer.Get("token"); v != "{{token}}" {
		t.Fatalf("bearer %q", v)
	}
	if len(get.ParamsPath) != 1 || get.ParamsPath[0].Value != "42" {
		t.Fatalf("path params %+v", get.ParamsPath)
	}
}

func TestParseOpenAPIBodiesAndWarnings(t *testing.T) {
	c := loadPetstore(t)
	order := findReq(c.Requests, "Place an order")
	if order == nil || order.Body != "form-urlencoded" {
		t.Fatalf("order %+v", order)
	}
	if v, _ := order.BodyFormURLEnc.Get("quantity"); v != "1" {
		t.Fatalf("form %+v", order.BodyFormURLEnc)
	}
	photo := findReq(c.Requests, "Upload a photo")
	if photo == nil || photo.Body != "multipart-form" {
		t.Fatalf("photo %+v", photo)
	}
	if _, ok := photo.BodyMultipart.Get("photo"); ok {
		t.Fatal("binary field should not be imported")
	}
	if v, _ := photo.BodyMultipart.Get("caption"); v != "" {
		t.Fatalf("caption %q", v)
	}
	del := findReq(c.Requests, "Delete with cookie")
	if del == nil || del.Method != "delete" {
		t.Fatalf("delete %+v", del)
	}

	all := strings.Join(c.Warnings, "\n")
	for _, want := range []string{"只导入第一个服务器地址", "Cookie 参数 \"session\"", "文件字段 \"photo\""} {
		if !strings.Contains(all, want) {
			t.Fatalf("warnings missing %q:\n%s", want, all)
		}
	}
}

func TestParseOpenAPIJSONAndRejects(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"J"},"paths":{"/a":{"get":{"operationId":"getA","security":[{"oauth":[]}],
	  "responses":{"200":{"description":"ok"}}}}},
	  "components":{"securitySchemes":{"oauth":{"type":"oauth2","flows":{}}}}}`
	c, err := ParseOpenAPI([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Requests) != 1 || c.Requests[0].Meta.Name != "getA" || c.Requests[0].Auth != "none" {
		t.Fatalf("requests %+v", c.Requests)
	}
	if v, _ := c.Bru.Vars.Get("baseUrl"); v != "" {
		t.Fatalf("baseUrl %q", v)
	}
	if !strings.Contains(strings.Join(c.Warnings, "\n"), "没有服务器地址") {
		t.Fatalf("warnings %v", c.Warnings)
	}

	if _, err := ParseOpenAPI([]byte(`{"swagger":"2.0","paths":{}}`)); err == nil || !strings.Contains(err.Error(), "Swagger") {
		t.Fatalf("swagger err %v", err)
	}
	if _, err := ParseOpenAPI([]byte(`{"openapi":"2.0","paths":{}}`)); err == nil {
		t.Fatal("expected error for unsupported version")
	}
	if _, err := ParseOpenAPI([]byte(`{"info":{}}`)); err == nil {
		t.Fatal("expected error for missing openapi field")
	}
}

func TestParseOpenAPIRecursiveSchemaStops(t *testing.T) {
	doc := `openapi: 3.0.0
info: {title: R}
paths:
  /n:
    post:
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Node'
      responses: {}
components:
  schemas:
    Node:
      type: object
      properties:
        child:
          $ref: '#/components/schemas/Node'
`
	c, err := ParseOpenAPI([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Requests) != 1 || c.Requests[0].Body != "json" {
		t.Fatalf("requests %+v", c.Requests)
	}
}

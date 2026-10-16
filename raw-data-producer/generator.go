package main

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ankitw/api-request-analyzer/raw-data-producer/internal/db"
)

type route struct {
	method   string
	template string // contains {id} where applicable
	service  string
	weight   int
}

var routes = []route{
	{"GET", "/api/orders/{id}", "orders-service", 20},
	{"GET", "/api/orders", "orders-service", 10},
	{"POST", "/api/orders", "orders-service", 6},
	{"PUT", "/api/orders/{id}", "orders-service", 3},
	{"DELETE", "/api/orders/{id}", "orders-service", 1},
	{"GET", "/api/users/{id}", "users-service", 14},
	{"PATCH", "/api/users/{id}", "users-service", 3},
	{"POST", "/api/auth/login", "auth-service", 5},
	{"GET", "/api/products/{id}", "catalog-service", 16},
	{"GET", "/api/products", "catalog-service", 8},
	{"POST", "/api/payments", "payments-service", 4},
	{"GET", "/api/payments/{id}", "payments-service", 3},
	{"GET", "/api/search", "search-service", 7},
}

var userAgents = []string{"browser", "mobile", "sdk", "bot"}
var userAgentWeights = []int{50, 30, 15, 5}

var errorTypeByStatus = map[int16][]string{
	400: {"validation"}, 401: {"unauthorized"}, 403: {"unauthorized"},
	404: {"not_found"}, 422: {"validation"}, 429: {"rate_limited"},
	500: {"internal_error"}, 502: {"dependency_failure"},
	503: {"dependency_failure"}, 504: {"timeout"},
}

type Generator struct {
	rng        *rand.Rand
	tenantZipf *rand.Zipf
	// tenantUsers[i] holds the user IDs owned by tenant i+1; each user belongs to exactly one tenant.
	tenantUsers [][]int64
	userZipf    []*rand.Zipf // per tenant, picks an index into tenantUsers
	routeCum    []int
	uaCum       []int
}

func NewGenerator(seed int64, tenants, users int) *Generator {
	rng := rand.New(rand.NewSource(seed))
	g := &Generator{
		rng: rng,
		// Zipf gives a few heavy tenants/users and a long tail.
		tenantZipf:  rand.NewZipf(rng, 1.2, 1, uint64(tenants-1)),
		tenantUsers: make([][]int64, tenants),
		userZipf:    make([]*rand.Zipf, tenants),
		routeCum:    cumulative(func(i int) int { return routes[i].weight }, len(routes)),
		uaCum:       cumulative(func(i int) int { return userAgentWeights[i] }, len(userAgents)),
	}
	// Assign users to tenants: one each first so no tenant is empty, the rest by tenant weight.
	for u := int64(1); u <= int64(users); u++ {
		t := int(u - 1)
		if t >= tenants {
			t = int(g.tenantZipf.Uint64())
		}
		g.tenantUsers[t] = append(g.tenantUsers[t], u)
	}
	for t, us := range g.tenantUsers {
		if len(us) > 1 {
			g.userZipf[t] = rand.NewZipf(rng, 1.1, 1, uint64(len(us)-1))
		}
	}
	return g
}

func cumulative(w func(int) int, n int) []int {
	out := make([]int, n)
	sum := 0
	for i := 0; i < n; i++ {
		sum += w(i)
		out[i] = sum
	}
	return out
}

func pick(rng *rand.Rand, cum []int) int {
	r := rng.Intn(cum[len(cum)-1])
	for i, c := range cum {
		if r < c {
			return i
		}
	}
	return len(cum) - 1
}

func (g *Generator) uuid() uuid.UUID {
	var u uuid.UUID
	g.rng.Read(u[:])
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return u
}

func (g *Generator) status() int16 {
	r := g.rng.Float64()
	switch {
	case r < 0.90:
		return []int16{200, 200, 200, 201, 204}[g.rng.Intn(5)]
	case r < 0.93:
		return 304
	case r < 0.98:
		return []int16{400, 401, 403, 404, 404, 422, 429}[g.rng.Intn(7)]
	default:
		return []int16{500, 502, 503, 504}[g.rng.Intn(4)]
	}
}

// Next builds one row stamped with t.
func (g *Generator) Next(t time.Time) db.InsertAPIRequestsParams {
	rt := routes[pick(g.rng, g.routeCum)]
	uri := rt.template
	if i := strings.Index(uri, "{id}"); i >= 0 {
		uri = uri[:i] + fmt.Sprint(1+g.rng.Intn(5_000_000)) + uri[i+4:]
	}
	if rt.template == "/api/search" {
		uri += fmt.Sprintf("?q=term%d", g.rng.Intn(1000))
	}

	status := g.status()
	var errType pgtype.Text
	if types, ok := errorTypeByStatus[status]; ok {
		errType = pgtype.Text{String: types[g.rng.Intn(len(types))], Valid: true}
	}

	// Log-normal latency; slow for errors/timeouts.
	lat := math.Exp(g.rng.NormFloat64()*0.7 + 4.5) // median ~90ms
	if status >= 500 {
		lat *= 5
	}
	if errType.String == "timeout" {
		lat = 30000
	}

	var reqSize, respSize int32
	if rt.method == "POST" || rt.method == "PUT" || rt.method == "PATCH" {
		reqSize = int32(200 + g.rng.ExpFloat64()*2000)
	} else {
		reqSize = int32(100 + g.rng.Intn(300))
	}
	if status == 204 || status == 304 {
		respSize = 0
	} else if status >= 400 {
		respSize = int32(100 + g.rng.Intn(400))
	} else {
		respSize = int32(300 + g.rng.ExpFloat64()*8000)
	}

	tenant := int(g.tenantZipf.Uint64())
	users := g.tenantUsers[tenant]
	idx := 0
	if z := g.userZipf[tenant]; z != nil {
		idx = int(z.Uint64())
	}
	user := users[idx]
	return db.InsertAPIRequestsParams{
		RequestID:         g.uuid(),
		TenantID:          int32(tenant) + 1,
		UserID:            user,
		RequestTime:       t,
		Method:            rt.method,
		Uri:               uri,
		RouteTemplate:     rt.template,
		StatusCode:        status,
		ResponseTimeMs:    int32(lat),
		RequestSizeBytes:  reqSize,
		ResponseSizeBytes: respSize,
		ServiceName:       rt.service,
		TraceID:           g.uuid(),
		ErrorType:         errType,
		// Stable per-user synthetic client hash.
		ClientIpHash:    fmt.Sprintf("%016x", uint64(user)*0x9E3779B97F4A7C15),
		UserAgentFamily: userAgents[pick(g.rng, g.uaCum)],
		IsRetry:         status >= 500 && g.rng.Float64() < 0.4 || g.rng.Float64() < 0.02,
	}
}

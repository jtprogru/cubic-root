package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/felixge/httpsnoop"
)

const (
	// routeUnmatched — http_route для запросов, которые ServeMux не сопоставил
	// ни с одним маршрутом (404, 405).
	routeUnmatched = "unmatched"
	// methodOther — http_request_method для нестандартных методов, как в OTel
	// semantic conventions. Иначе метод из запроса создавал бы новые серии.
	methodOther = "_OTHER"
)

var knownMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {},
	http.MethodPut: {}, http.MethodPatch: {}, http.MethodDelete: {},
	http.MethodConnect: {}, http.MethodOptions: {}, http.MethodTrace: {},
}

// instrumentHTTP снимает RED-метрики со всех запросов к next, включая те, что
// ServeMux отклонил сам (404, 405) и до хендлеров не дошли.
//
// http_route берётся из r.Pattern, который ServeMux записывает в запрос при
// маршрутизации. В label попадают только паттерны из patterns: для
// CONNECT-редиректов ServeMux кладёт в r.Pattern путь из самого запроса, и без
// этой проверки клиент мог бы создавать произвольные серии.
func instrumentHTTP(next http.Handler, m *metrics, patterns ...string) http.Handler {
	routes := make(map[string]string, len(patterns))
	for _, p := range patterns {
		routes[p] = routeFromPattern(p)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := methodLabel(r.Method)
		active := m.activeRequests.WithLabelValues(method)
		active.Inc()
		defer active.Dec()

		res := httpsnoop.CaptureMetrics(next, w, r)

		route, ok := routes[r.Pattern]
		if !ok {
			route = routeUnmatched
		}
		m.requestDuration.WithLabelValues(method, route, strconv.Itoa(res.Code)).Observe(res.Duration.Seconds())
		m.responseSize.WithLabelValues(route).Observe(float64(res.Written))
	})
}

// routeFromPattern убирает метод из паттерна ServeMux: "GET /cubic-root" →
// "/cubic-root".
func routeFromPattern(pattern string) string {
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}

func methodLabel(method string) string {
	if _, ok := knownMethods[method]; ok {
		return method
	}
	return methodOther
}

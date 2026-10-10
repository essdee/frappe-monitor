package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseFrappeMonitorRequestAndLineProtocol(t *testing.T) {
	line := `{"site":"shop.example","timestamp":"2026-08-13 09:30:12.123456+00:00","transaction_type":"request","duration":807142,"request":{"method":"GET","path":"/api/resource/Sales%20Invoice/ACC-SINV-0001?fields=%5B%5D","status_code":500,"response_length":9687,"ip":"10.0.0.8"},"uuid":"never-a-label","exception":"frappe.ValidationError: invalid document"}`
	event, err := ParseFrappeMonitorEvent(line)
	require.NoError(t, err)
	require.Equal(t, "shop.example", event.Site)
	require.Equal(t, int64(500), event.StatusCode)
	require.Equal(t, time.Date(2026, 8, 13, 9, 30, 12, 123456000, time.UTC), event.Timestamp)

	body := event.LineProtocol("prod a", "frappe-15", event.Timestamp)
	require.Contains(t, body, `frappe_site_request_duration_seconds,server=prod\ a,bench=frappe-15,site=shop.example value=0.807142`)
	require.Contains(t, body, `frappe_site_request_route_duration_seconds,server=prod\ a,bench=frappe-15,site=shop.example,method=GET,route=/api/resource/Sales\ Invoice/:name,kind=api value=0.807142`)
	require.Contains(t, body, `frappe_site_request_status,server=prod\ a,bench=frappe-15,site=shop.example,status=500,status_class=5xx value=1`)
	require.Contains(t, body, `frappe_site_request_route_error,server=prod\ a,bench=frappe-15,site=shop.example,method=GET,route=/api/resource/Sales\ Invoice/:name,kind=api,status=500,status_class=5xx value=1`)
	require.Contains(t, body, `frappe_site_request_response_bytes`)
	require.Contains(t, body, `frappe_site_transaction_error,server=prod\ a,bench=frappe-15,site=shop.example,transaction=request,exception=frappe.ValidationError value=1`)
	require.NotContains(t, body, "never-a-label")
	require.NotContains(t, body, "10.0.0.8")
}

func TestParseFrappeMonitorJob(t *testing.T) {
	event, err := ParseFrappeMonitorEvent(`{"site":"erp.example","timestamp":"2026-08-13 15:00:00.123456","transaction_type":"job","duration":2500000,"job":{"method":"erpnext.stock.do_work","scheduled":true,"wait":125000}}`)
	require.NoError(t, err)
	require.True(t, event.Timestamp.IsZero(), "naive remote timestamps must not be guessed")
	body := event.LineProtocol("prod", "frappe-16", time.Unix(100, 0).UTC())
	require.Contains(t, body, `frappe_site_job_duration_seconds,server=prod,bench=frappe-16,site=erp.example value=2.5`)
	require.Contains(t, body, `frappe_site_job_method_duration_seconds,server=prod,bench=frappe-16,site=erp.example,method=erpnext.stock.do_work,scheduled=true value=2.5`)
	require.Contains(t, body, `frappe_site_job_wait_seconds`)
}

func TestNormalizeFrappeRouteBoundsCardinality(t *testing.T) {
	tests := map[string]string{
		"/api/method/frappe.desk.form.load.getdoc": "/api/method/frappe.desk.form.load.getdoc",
		"/api/resource/Customer/CUST-999":          "/api/resource/Customer/:name",
		"/api/v2/document/Task/TASK-1":             "/api/v2/document/Task/:name",
		"/orders/customer-specific-slug":           "/_web",
	}
	for input, expected := range tests {
		route, _ := normalizeFrappeRoute(input)
		require.Equal(t, expected, route)
	}
	veryLong := "/api/method/" + strings.Repeat("x", 1000)
	route, _ := normalizeFrappeRoute(veryLong)
	require.LessOrEqual(t, len(route), 300)
}

func TestParseFrappeMonitorRejectsInvalidShape(t *testing.T) {
	_, err := ParseFrappeMonitorEvent(`{"transaction_type":"request","duration":1}`)
	require.ErrorContains(t, err, "no site")
	_, err = ParseFrappeMonitorEvent(`{"site":"x","transaction_type":"socket","duration":1}`)
	require.ErrorContains(t, err, "unsupported")
}

-- name: InsertAPIRequests :copyfrom
INSERT INTO api_requests (
    request_id, tenant_id, user_id, request_time, method, uri,
    route_template, status_code, response_time_ms, request_size_bytes,
    response_size_bytes, service_name, trace_id, error_type,
    client_ip_hash, user_agent_family, is_retry
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
);

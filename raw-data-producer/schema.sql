CREATE TABLE IF NOT EXISTS api_requests (
    request_id          UUID        PRIMARY KEY,
    tenant_id           INT         NOT NULL,
    user_id             BIGINT      NOT NULL,
    request_time        TIMESTAMPTZ NOT NULL,
    method              VARCHAR(10) NOT NULL,
    uri                 TEXT        NOT NULL,
    route_template      VARCHAR(255) NOT NULL,
    status_code         SMALLINT    NOT NULL,
    response_time_ms    INT         NOT NULL,
    request_size_bytes  INT         NOT NULL,
    response_size_bytes INT         NOT NULL,
    service_name        VARCHAR(64) NOT NULL,
    trace_id            UUID        NOT NULL,
    error_type          VARCHAR(64),
    client_ip_hash      VARCHAR(64) NOT NULL,
    user_agent_family   VARCHAR(32) NOT NULL,
    is_retry            BOOLEAN     NOT NULL
);

-- Indexes for the time-series benchmark queries.
CREATE INDEX IF NOT EXISTS idx_api_requests_time        ON api_requests (request_time);
CREATE INDEX IF NOT EXISTS idx_api_requests_tenant_time ON api_requests (tenant_id, request_time);

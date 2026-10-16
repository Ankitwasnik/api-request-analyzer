FROM postgres:16

# Creates the api_requests table on first start (empty data dir only).
COPY raw-data-producer/schema.sql /docker-entrypoint-initdb.d/01-schema.sql

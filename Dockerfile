# syntax=docker/dockerfile:1

# ---------- build ----------
FROM golang:1.27-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=1
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

# ---------- runtime ----------
FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates tzdata libsqlite3-0 python3 python3-venv \
    && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/venv
ENV PATH="/opt/venv/bin:$PATH"
COPY requirements-lab.txt /tmp/requirements-lab.txt
RUN pip install --no-cache-dir -r /tmp/requirements-lab.txt && rm /tmp/requirements-lab.txt

WORKDIR /app
COPY --from=build /out/bot /app/bot
COPY agent/public /app/agent/public
COPY Backtest_Lab /app/Backtest_Lab
COPY model.json /app/model.json
COPY predict.py model_info.py /app/
RUN mkdir -p /app/data /app/activity_logs

ENV BIND_ADDR=0.0.0.0 \
    LAB_DIR=/app/Backtest_Lab \
    PYTHON_PATH=/opt/venv/bin/python \
    DATABASE_PATH=/app/data/prophet_trader.db

EXPOSE 4534
ENTRYPOINT ["/app/bot"]

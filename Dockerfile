FROM python:3.12-slim

WORKDIR /app

# Install uv for fast, reliable dependency installation
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

# Copy manifest files first to leverage layer caching
COPY pyproject.toml .
# Stub the package so uv can resolve deps without the full source tree
RUN mkdir -p app && touch app/__init__.py
RUN uv pip install --system --no-cache .

# Now copy the real application code (overwrites stub)
COPY app/ ./app/
COPY config.json .

# Sources file is stored in the named volume at /data
VOLUME ["/data"]

ENV CONDUIT_DATA_DIR=/data
ENV PYTHONUNBUFFERED=1

EXPOSE 8000

CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]

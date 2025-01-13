FROM python:3.9-slim AS builder

WORKDIR /code

COPY ./pyproject.toml /code/pyproject.toml

RUN pip install "poetry==2.0.0"

RUN poetry self add poetry-plugin-export

RUN poetry export -f requirements.txt --output requirements.txt

FROM python:3.9-slim AS runner

RUN apt-get update && \
    apt-get install -y postgresql-client && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY . /app

RUN pip install --no-cache-dir -r requirements.txt

ENV PYTHONUNBUFFERED=1

CMD ["python", "app.py"]

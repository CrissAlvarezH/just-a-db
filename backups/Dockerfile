FROM python:3.9-slim

RUN apt-get update && \
    apt-get install -y postgresql-client && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY . /app

RUN pip install --no-cache-dir -r requirements.txt

ENV PYTHONUNBUFFERED=1

CMD ["python", "app.py"]

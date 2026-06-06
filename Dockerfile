FROM python:3.12-slim

WORKDIR /app

RUN pip install --no-cache-dir requests urllib3 psycopg2-binary beautifulsoup4

COPY scraper_worker.py .

CMD ["python", "-u", "scraper_worker.py"]

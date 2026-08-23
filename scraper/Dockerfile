FROM python:3.12-slim

WORKDIR /app

RUN pip install --no-cache-dir curl_cffi psycopg2-binary

COPY scraper_worker.py dork_parser.py shopify_dorks.txt ./

CMD ["python", "-u", "scraper_worker.py"]

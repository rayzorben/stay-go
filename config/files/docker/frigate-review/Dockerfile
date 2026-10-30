# Debian trixie: ffmpeg 7.1 + Mesa 25 (VA-API for RDNA4 / RX 9070).
FROM python:3.13-slim-trixie
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg mesa-va-drivers vainfo \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY app/ /app/
RUN chmod -R a+rX /app
EXPOSE 5080
CMD ["python3", "-u", "/app/server.py"]

FROM python:3.13-slim-bookworm

ARG BOTO3_VERSION="1.35.99"

RUN pip install --no-cache-dir "boto3==${BOTO3_VERSION}" \
    && groupadd --gid 10001 broker \
    && useradd --uid 10001 --gid broker --create-home --shell /usr/sbin/nologin broker \
    && mkdir -p /home/broker/.aws/sso/cache \
    && chown -R broker:broker /home/broker

COPY --chown=broker:broker broker.py /opt/aws-credential-broker/broker.py

ENV HOME=/home/broker \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

USER broker
EXPOSE 9911
ENTRYPOINT ["python", "/opt/aws-credential-broker/broker.py"]

# syntax=docker/dockerfile:1.6
FROM python:3.12-slim

WORKDIR /app
COPY oracle/requirements.txt /app/
RUN pip install --no-cache-dir -r requirements.txt

COPY proto/ /proto/
COPY oracle/oracle.py /app/

# Generate stubs into the app dir so `import trading_pb2` works.
RUN python -m grpc_tools.protoc -I /proto \
      --python_out=/app \
      --grpc_python_out=/app \
      /proto/trading.proto

EXPOSE 7000
CMD ["python", "/app/oracle.py"]

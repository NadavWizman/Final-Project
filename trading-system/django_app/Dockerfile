# syntax=docker/dockerfile:1.6
FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY django_app/requirements.txt /app/
RUN pip install --no-cache-dir -r requirements.txt

COPY proto/ /app/proto/
COPY django_app/ /app/

# Generate Python protobuf + gRPC stubs into django_app/generated/
RUN python -m grpc_tools.protoc -I /app/proto \
      --python_out=/app/generated \
      --grpc_python_out=/app/generated \
      /app/proto/trading.proto \
 && touch /app/generated/__init__.py

# grpc_tools generates `import trading_pb2` in the *_grpc.py file, which is
# a relative import. Patch it so it works when we import as
# `from generated import trading_pb2_grpc`.
RUN sed -i 's/^import trading_pb2/from . import trading_pb2/' /app/generated/trading_pb2_grpc.py

EXPOSE 8000
CMD ["python", "manage.py", "runserver", "0.0.0.0:8000"]

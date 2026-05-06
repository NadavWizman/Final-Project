"""Custom user model.

Each user has an ECDSA P-256 keypair. The **private** key lives in the
Django DB for the MVP so the backend can sign orders on the user's behalf.
In production you'd move this to an HSM / KMS, or push signing to the client.
"""
from __future__ import annotations

from django.contrib.auth.models import AbstractUser
from django.db import models


class User(AbstractUser):
    # user_id is what we put into the OrderTx on the wire — keep it stable
    # and independent of the PK so we can rotate the DB without breaking
    # replay protection.
    external_id = models.CharField(max_length=64, unique=True)

    # PEM-encoded PKCS8 private key. Encrypt at rest in production.
    priv_key_pem = models.TextField()
    # PEM-encoded SubjectPublicKeyInfo.
    pub_key_pem = models.TextField()

    # Monotonic nonce counter. Increment BEFORE signing each tx.
    nonce = models.BigIntegerField(default=0)

    def __str__(self) -> str:
        return self.external_id or self.username

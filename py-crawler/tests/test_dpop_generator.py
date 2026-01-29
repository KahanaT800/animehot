"""Tests for DPoP token generator."""

import base64
import json
import time

import pytest

from mercari_crawler.dpop_generator import DPoPCredentials, DPoPGenerator


class TestDPoPCredentials:
    """Tests for DPoPCredentials dataclass."""

    def test_creation(self):
        creds = DPoPCredentials(
            x="test_x",
            y="test_y",
            device_uuid="uuid-123",
            session_id="session-456",
            created_at=1706500000.0,
        )

        assert creds.x == "test_x"
        assert creds.y == "test_y"
        assert creds.device_uuid == "uuid-123"
        assert creds.session_id == "session-456"
        assert creds.created_at == 1706500000.0

    def test_to_dict(self):
        creds = DPoPCredentials(
            x="x_val",
            y="y_val",
            device_uuid="uuid",
            session_id="session",
            created_at=1000.0,
        )

        result = creds.to_dict()

        assert result["x"] == "x_val"
        assert result["y"] == "y_val"
        assert result["device_uuid"] == "uuid"
        assert result["session_id"] == "session"
        assert result["created_at"] == 1000.0

    def test_from_dict(self):
        data = {
            "x": "x_val",
            "y": "y_val",
            "device_uuid": "uuid",
            "session_id": "session",
            "created_at": 2000.0,
        }

        creds = DPoPCredentials.from_dict(data)

        assert creds.x == "x_val"
        assert creds.device_uuid == "uuid"
        assert creds.created_at == 2000.0


class TestDPoPGenerator:
    """Tests for DPoPGenerator."""

    def test_initialization(self):
        """Test that generator initializes with valid keys."""
        generator = DPoPGenerator()

        # Should have generated UUIDs
        assert generator.device_uuid is not None
        assert len(generator.device_uuid) == 36  # UUID format
        assert generator.session_id is not None
        assert len(generator.session_id) == 32  # UUID hex format

    def test_initialization_with_custom_ids(self):
        """Test initialization with custom device/session IDs."""
        generator = DPoPGenerator(
            device_uuid="custom-device-uuid",
            session_id="custom-session-id",
        )

        assert generator.device_uuid == "custom-device-uuid"
        assert generator.session_id == "custom-session-id"

    def test_credentials_property(self):
        """Test credentials property returns valid DPoPCredentials."""
        generator = DPoPGenerator()
        creds = generator.credentials

        assert isinstance(creds, DPoPCredentials)
        assert creds.x is not None
        assert creds.y is not None
        assert creds.device_uuid == generator.device_uuid
        assert creds.session_id == generator.session_id

    def test_age_seconds(self):
        """Test age_seconds returns reasonable value."""
        generator = DPoPGenerator()

        # Should be close to 0 right after creation
        age = generator.age_seconds()
        assert age >= 0
        assert age < 1  # Less than 1 second

    def test_generate_returns_jwt(self):
        """Test that generate returns a valid JWT format."""
        generator = DPoPGenerator()

        token = generator.generate("POST", "https://api.mercari.jp/v2/entities:search")

        # JWT has 3 parts separated by dots
        parts = token.split(".")
        assert len(parts) == 3

        # All parts should be base64url encoded
        for part in parts:
            assert all(c in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" for c in part)

    def test_generate_header_structure(self):
        """Test that generated token has correct header structure."""
        generator = DPoPGenerator()
        token = generator.generate("POST", "https://example.com/api")

        # Decode header
        header_b64 = token.split(".")[0]
        # Add padding for base64 decoding
        padding = 4 - len(header_b64) % 4
        if padding != 4:
            header_b64 += "=" * padding
        header = json.loads(base64.urlsafe_b64decode(header_b64))

        assert header["typ"] == "dpop+jwt"
        assert header["alg"] == "ES256"
        assert "jwk" in header
        assert header["jwk"]["kty"] == "EC"
        assert header["jwk"]["crv"] == "P-256"
        assert "x" in header["jwk"]
        assert "y" in header["jwk"]

    def test_generate_payload_structure(self):
        """Test that generated token has correct payload structure."""
        generator = DPoPGenerator()
        url = "https://api.mercari.jp/test"
        method = "POST"

        token = generator.generate(method, url)

        # Decode payload
        payload_b64 = token.split(".")[1]
        padding = 4 - len(payload_b64) % 4
        if padding != 4:
            payload_b64 += "=" * padding
        payload = json.loads(base64.urlsafe_b64decode(payload_b64))

        assert payload["htm"] == method
        assert payload["htu"] == url
        assert "iat" in payload
        assert "jti" in payload
        assert payload["uuid"] == generator.device_uuid

        # iat should be recent timestamp
        assert abs(payload["iat"] - int(time.time())) < 5

    def test_generate_unique_jti(self):
        """Test that each token has unique jti."""
        generator = DPoPGenerator()
        url = "https://example.com/api"

        tokens = [generator.generate("POST", url) for _ in range(5)]

        # Extract jti from each token
        jtis = []
        for token in tokens:
            payload_b64 = token.split(".")[1]
            padding = 4 - len(payload_b64) % 4
            if padding != 4:
                payload_b64 += "=" * padding
            payload = json.loads(base64.urlsafe_b64decode(payload_b64))
            jtis.append(payload["jti"])

        # All jti should be unique
        assert len(set(jtis)) == 5

    def test_generate_different_methods(self):
        """Test generating tokens for different HTTP methods."""
        generator = DPoPGenerator()
        url = "https://example.com/api"

        for method in ["GET", "POST", "PUT", "DELETE"]:
            token = generator.generate(method, url)

            payload_b64 = token.split(".")[1]
            padding = 4 - len(payload_b64) % 4
            if padding != 4:
                payload_b64 += "=" * padding
            payload = json.loads(base64.urlsafe_b64decode(payload_b64))

            assert payload["htm"] == method

    def test_signature_length(self):
        """Test that signature has correct length for ES256."""
        generator = DPoPGenerator()
        token = generator.generate("POST", "https://example.com")

        signature_b64 = token.split(".")[2]
        # Add padding
        padding = 4 - len(signature_b64) % 4
        if padding != 4:
            signature_b64 += "=" * padding
        signature = base64.urlsafe_b64decode(signature_b64)

        # ES256 signature is r || s, each 32 bytes = 64 bytes total
        assert len(signature) == 64

    def test_public_key_coordinates_length(self):
        """Test that public key coordinates are correct length."""
        generator = DPoPGenerator()
        creds = generator.credentials

        # Decode x and y
        x_b64 = creds.x
        y_b64 = creds.y

        # Add padding
        for b64 in [x_b64, y_b64]:
            padding = 4 - len(b64) % 4
            if padding != 4:
                b64 += "=" * padding
            decoded = base64.urlsafe_b64decode(b64 + "=" * (4 - len(b64) % 4) if len(b64) % 4 else b64)
            # P-256 coordinates are 32 bytes each
            assert len(decoded) == 32

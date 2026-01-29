"""DPoP Token 生成器

基于逆向分析实现，完全不需要浏览器。
使用 EC P-256 密钥对 + JWT 签名。
"""

import base64
import json
import time
import uuid
from dataclasses import dataclass, field

from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature


def _b64_encode(data: bytes) -> str:
    """Base64 URL encode without padding."""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


@dataclass
class DPoPCredentials:
    """DPoP 凭证，包含生成 token 所需的所有信息"""

    # EC P-256 密钥对的公钥坐标 (Base64 URL encoded)
    x: str
    y: str
    # 设备标识
    device_uuid: str
    # 会话标识
    session_id: str
    # 创建时间
    created_at: float = field(default_factory=time.time)

    def to_dict(self) -> dict:
        return {
            "x": self.x,
            "y": self.y,
            "device_uuid": self.device_uuid,
            "session_id": self.session_id,
            "created_at": self.created_at,
        }

    @classmethod
    def from_dict(cls, data: dict) -> "DPoPCredentials":
        return cls(**data)


class DPoPGenerator:
    """DPoP Token 生成器

    每个实例维护一个 EC P-256 密钥对，可以生成多个 DPoP token。
    密钥对应该定期轮换（建议每 15 分钟）以模拟浏览器行为。
    """

    def __init__(self, device_uuid: str | None = None, session_id: str | None = None):
        """初始化生成器

        Args:
            device_uuid: 设备 UUID，不传则随机生成
            session_id: 会话 ID，不传则随机生成
        """
        # 生成 EC P-256 密钥对
        self._private_key = ec.generate_private_key(ec.SECP256R1(), default_backend())
        public_numbers = self._private_key.public_key().public_numbers()

        # 公钥坐标 (32 bytes each for P-256)
        x_bytes = public_numbers.x.to_bytes(32, byteorder="big")
        y_bytes = public_numbers.y.to_bytes(32, byteorder="big")

        self._x_b64 = _b64_encode(x_bytes)
        self._y_b64 = _b64_encode(y_bytes)

        # 设备和会话标识
        self._device_uuid = device_uuid or str(uuid.uuid4())
        self._session_id = session_id or uuid.uuid4().hex

        self._created_at = time.time()

    @property
    def credentials(self) -> DPoPCredentials:
        """获取当前凭证信息"""
        return DPoPCredentials(
            x=self._x_b64,
            y=self._y_b64,
            device_uuid=self._device_uuid,
            session_id=self._session_id,
            created_at=self._created_at,
        )

    @property
    def device_uuid(self) -> str:
        return self._device_uuid

    @property
    def session_id(self) -> str:
        return self._session_id

    def age_seconds(self) -> float:
        """返回密钥对的年龄（秒）"""
        return time.time() - self._created_at

    def generate(self, method: str, url: str) -> str:
        """生成 DPoP token

        Args:
            method: HTTP 方法 (GET, POST, etc.)
            url: 目标 URL

        Returns:
            JWT 格式的 DPoP token
        """
        # JWT Header
        header = {
            "typ": "dpop+jwt",
            "alg": "ES256",
            "jwk": {
                "kty": "EC",
                "crv": "P-256",
                "x": self._x_b64,
                "y": self._y_b64,
            },
        }

        # JWT Payload
        payload = {
            "iat": int(time.time()),
            "jti": str(uuid.uuid4()),
            "htu": url,
            "htm": method,
            "uuid": self._device_uuid,
        }

        # Encode header and payload
        header_b64 = _b64_encode(json.dumps(header, separators=(",", ":")).encode())
        payload_b64 = _b64_encode(json.dumps(payload, separators=(",", ":")).encode())

        # Sign
        message = f"{header_b64}.{payload_b64}".encode()
        signature_der = self._private_key.sign(message, ec.ECDSA(hashes.SHA256()))

        # Convert DER signature to raw (r || s) format
        r, s = decode_dss_signature(signature_der)
        r_bytes = r.to_bytes(32, byteorder="big")
        s_bytes = s.to_bytes(32, byteorder="big")
        signature_b64 = _b64_encode(r_bytes + s_bytes)

        return f"{header_b64}.{payload_b64}.{signature_b64}"

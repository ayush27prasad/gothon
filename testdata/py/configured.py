"""Fixture for pydantic-settings support."""

import os

from pydantic import BaseModel, Field, SecretStr
from pydantic_settings import BaseSettings, SettingsConfigDict

import gothon


class Settings(BaseSettings):
    """Configuration for the configured fixture."""

    model_config = SettingsConfigDict(env_prefix="CONFIGURED_")

    api_key: SecretStr = Field(description="Credential for an external service.")
    base_url: str = "https://api.example.com"
    retries: int = 3
    debug: bool = True
    tags: list[str] = []
    region: str = Field("eu", alias="APP_REGION")


settings = Settings()

LEGACY = os.getenv("LEGACY_TOKEN")  # flagged by gothon gen


class Info(BaseModel):
    key_length: int
    base_url: str
    retries: int
    debug: bool
    tags: list[str]
    region: str


@gothon.task
def info() -> Info:
    return Info(
        key_length=len(settings.api_key.get_secret_value()),
        base_url=settings.base_url,
        retries=settings.retries,
        debug=settings.debug,
        tags=settings.tags,
        region=settings.region,
    )

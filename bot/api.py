# обращения к api router-toggle
import aiohttp


class APIError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message


class API:
    def __init__(self, base_url: str, code: str):
        self.base_url = base_url.rstrip("/")
        self.code = code
        self.timeout = aiohttp.ClientTimeout(total=180)

    async def routers(self) -> list[dict]:
        data = await self._post("/v1/routers", {})
        return data.get("routers") or []

    async def status(self, router_id: int) -> dict:
        return await self._post("/v1/status", {"router_id": router_id})

    async def apply(self, router_id: int, value: bool) -> dict:
        return await self._post(
            "/v1/apply",
            {"router_id": router_id, "op": "udp_proxy", "value": value},
        )

    async def log(self, limit: int = 20) -> list[dict]:
        data = await self._post("/v1/log", {"limit": limit})
        return data.get("entries") or []

    async def _post(self, path: str, payload: dict) -> dict:
        body = {"code": self.code, **payload}
        headers = {"X-Client-Version": "1"}

        async with aiohttp.ClientSession(timeout=self.timeout) as session:
            async with session.post(self.base_url + path, json=body, headers=headers) as resp:
                data = await resp.json(content_type=None)
                if resp.status != 200:
                    raise APIError(
                        data.get("code", "E-20"),
                        data.get("message", "Внутренняя ошибка"),
                    )
                return data

import aiohttp


class APIError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message


class API:
    def __init__(self, base_url: str):
        self.base_url = base_url.rstrip("/")
        self.timeout = aiohttp.ClientTimeout(total=180)

    async def routers(self, code: str) -> list[dict]:
        data = await self._post(code, "/v1/routers", {})
        return data.get("routers") or []

    async def status(self, code: str, router_id: int = 0) -> dict:
        return await self._post(code, "/v1/status", {"router_id": router_id})

    async def apply(self, code: str, op: str, value: bool, router_id: int = 0) -> dict:
        return await self._post(code, "/v1/apply", {"router_id": router_id, "op": op, "value": value})

    async def check(self, code: str, router_id: int = 0) -> dict:
        return await self._post(code, "/v1/check", {"router_id": router_id})

    async def reboot(self, code: str, router_id: int = 0) -> None:
        await self._post(code, "/v1/reboot", {"router_id": router_id})

    async def add_router(self, code: str, router: dict) -> dict:
        return await self._post(code, "/v1/routers/add", router)

    async def domains(self, code: str, router_id: int = 0) -> dict:
        return await self._post(code, "/v1/domains", {"router_id": router_id})

    async def search_domains(self, code: str, query: str, router_id: int = 0) -> dict:
        return await self._post(code, "/v1/domains/search", {"router_id": router_id, "query": query})

    async def add_domain(self, code: str, kind: str, name: str, router_id: int = 0) -> dict:
        return await self._post(code, "/v1/domains/add", {"router_id": router_id, "kind": kind, "name": name})

    async def remove_domain(self, code: str, kind: str, name: str, router_id: int = 0) -> dict:
        return await self._post(code, "/v1/domains/remove", {"router_id": router_id, "kind": kind, "name": name})

    async def rename_router(self, code: str, router_id: int, display_name: str) -> dict:
        return await self._post(code, "/v1/routers/rename", {"router_id": router_id, "display_name": display_name})

    async def router_code(self, code: str, router_id: int) -> str:
        data = await self._post(code, "/v1/routers/code", {"router_id": router_id})
        return data["access_code"]

    async def log(self, code: str, limit: int = 20) -> list[dict]:
        data = await self._post(code, "/v1/log", {"limit": limit})
        return data.get("entries") or []

    async def _post(self, code: str, path: str, payload: dict) -> dict:
        body = {"code": code, **payload}
        headers = {"X-Client-Version": "1"}

        async with aiohttp.ClientSession(timeout=self.timeout) as session:
            async with session.post(self.base_url + path, json=body, headers=headers) as resp:
                try:
                    data = await resp.json(content_type=None)
                except ValueError:
                    data = None
                if resp.status != 200:
                    # сервер не понял запрос
                    if not isinstance(data, dict) or "code" not in data:
                        raise APIError("E-20", "Сервер ответил неожиданно")
                    raise APIError(data["code"], data.get("message", ""))
                return data or {}

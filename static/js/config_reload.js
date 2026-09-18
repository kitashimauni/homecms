export async function reloadConfigAfterSync({
    siteID,
    generation,
    fetchConfig,
    isCurrent,
    applyConfig,
}) {
    const config = await fetchConfig(siteID);
    if (!isCurrent(siteID, generation)) return false;
    applyConfig(config);
    return true;
}

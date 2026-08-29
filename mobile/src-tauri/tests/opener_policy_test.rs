use serde_json::{json, Value};

#[test]
fn opener_capability_allows_only_http_and_https_urls() {
    let capability: Value =
        serde_json::from_str(include_str!("../capabilities/default.json")).unwrap();
    let permissions = capability["permissions"].as_array().unwrap();
    let opener_permissions: Vec<&Value> = permissions
        .iter()
        .filter(|permission| {
            permission
                .as_str()
                .or_else(|| permission.get("identifier").and_then(Value::as_str))
                .is_some_and(|identifier| identifier.starts_with("opener:"))
        })
        .collect();

    assert_eq!(
        opener_permissions,
        vec![&json!({
            "identifier": "opener:allow-open-url",
            "allow": [
                { "url": "http://*" },
                { "url": "https://*" }
            ]
        })]
    );
}

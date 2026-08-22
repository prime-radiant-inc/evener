use std::collections::BTreeSet;

use app_lib::commands::{
    AppwireChannelEvent, AppwireCloseRequest, AppwireOpenRequest, AppwireSendRequest,
    HubHttpExecuteRequest, HubHttpResponseMetadata,
};
use app_lib::http_transport::{HubResponseHeaders, PreparedHttpRequest, REQUEST_ID_HEADER};
use app_lib::profile_runtime::{
    CancelPreviewRequest, ConfirmPairingRequest, PreviewPasteRequest, PreviewRepairRequest,
    RemoveRequest, RenameRequest, SelectRequest,
};
use quote::ToTokens as _;
use serde::Serialize;
use serde_json::{json, Map, Value};
use syn::parse::Parser as _;
use syn::punctuated::Punctuated;
use syn::visit::Visit as _;
use syn::{FnArg, Item, Pat, Token, Type};

const COMMANDS_SOURCE: &str = include_str!("../src/commands.rs");
const REGISTRATION_SOURCE: &str = include_str!("../src/lib.rs");
const FIXTURE_SOURCE: &str = include_str!("../fixtures/command-envelopes-v1.json");

#[test]
fn syntax_derived_command_fixture_matches_actual_signatures_and_registration() {
    assert_committed_fixture(COMMANDS_SOURCE, REGISTRATION_SOURCE);
}

#[test]
#[should_panic(expected = "command fixture drifted")]
fn renaming_an_actual_rust_argument_breaks_fixture_agreement() {
    let renamed = COMMANDS_SOURCE.replacen(
        "request: AppwireOpenRequest",
        "open_request: AppwireOpenRequest",
        1,
    );
    assert_ne!(renamed, COMMANDS_SOURCE, "mutation did not reach source");
    assert_committed_fixture(&renamed, REGISTRATION_SOURCE);
}

fn assert_committed_fixture(commands_source: &str, registration_source: &str) {
    let generated = generate_fixture(commands_source, registration_source);
    let committed: Value = serde_json::from_str(FIXTURE_SOURCE).expect("parse fixture");
    assert_eq!(
        committed, generated,
        "command fixture drifted from actual Rust parameter names/types or serde field names"
    );
}

fn generate_fixture(commands_source: &str, registration_source: &str) -> Value {
    let commands_file = syn::parse_file(commands_source).expect("parse commands.rs");
    let mut generated_commands = Map::new();
    let mut defined = BTreeSet::new();

    for item in commands_file.items {
        let Item::Fn(function) = item else { continue };
        if !function.attrs.iter().any(|attribute| {
            attribute
                .path()
                .segments
                .last()
                .is_some_and(|s| s.ident == "command")
        }) {
            continue;
        }
        let command_name = function.sig.ident.to_string();
        defined.insert(command_name.clone());

        let mut args = Map::new();
        let mut raw_payload = false;
        for argument in &function.sig.inputs {
            let FnArg::Typed(argument) = argument else {
                panic!("commands may not have self receivers: {command_name}");
            };
            let Pat::Ident(pattern) = argument.pat.as_ref() else {
                panic!("command arguments must be identifiers: {command_name}");
            };
            let type_name = last_type_ident(&argument.ty);
            if type_name == "State" {
                continue;
            }
            if type_name == "Request" {
                assert_eq!(pattern.ident, "request", "raw IPC argument changed");
                raw_payload = true;
                continue;
            }
            let key = snake_to_lower_camel(&pattern.ident.to_string());
            let value = if type_name == "Channel" {
                Value::String("<channel>".to_owned())
            } else {
                sample_for_request_type(&type_name)
            };
            assert!(args.insert(key, value).is_none(), "duplicate argument");
        }

        let envelope = if raw_payload {
            assert!(args.is_empty(), "raw IPC command cannot mix JSON arguments");
            json!({
                "payload": "raw",
                "headers": { (REQUEST_ID_HEADER): "<requestId>" },
            })
        } else {
            json!({ "args": args })
        };
        generated_commands.insert(command_name, envelope);
    }

    let registered = registered_command_names(registration_source);
    assert_eq!(
        defined, registered,
        "actual #[tauri::command] functions and production generate_handler! registration drifted"
    );

    json!({
        "version": 1,
        "commands": generated_commands,
        "dtoFixtures": dto_fixtures(),
    })
}

fn registered_command_names(registration_source: &str) -> BTreeSet<String> {
    #[derive(Default)]
    struct GenerateHandlerVisitor {
        names: Vec<String>,
    }
    impl<'ast> syn::visit::Visit<'ast> for GenerateHandlerVisitor {
        fn visit_macro(&mut self, mac: &'ast syn::Macro) {
            if mac
                .path
                .segments
                .last()
                .is_some_and(|segment| segment.ident == "generate_handler")
            {
                let parser = Punctuated::<syn::Path, Token![,]>::parse_terminated;
                let paths = parser
                    .parse2(mac.tokens.clone())
                    .expect("parse generate_handler arguments");
                self.names.extend(paths.into_iter().map(|path| {
                    path.segments
                        .last()
                        .expect("registered command path")
                        .ident
                        .to_string()
                }));
            }
            syn::visit::visit_macro(self, mac);
        }
    }

    let file = syn::parse_file(registration_source).expect("parse lib.rs");
    let mut visitor = GenerateHandlerVisitor::default();
    visitor.visit_file(&file);
    assert_eq!(
        visitor.names.len(),
        visitor.names.iter().collect::<BTreeSet<_>>().len(),
        "duplicate production command registration"
    );
    visitor.names.into_iter().collect()
}

fn last_type_ident(ty: &Type) -> String {
    match ty {
        Type::Path(path) => path
            .path
            .segments
            .last()
            .expect("command argument type path")
            .ident
            .to_string(),
        other => panic!(
            "unsupported command argument type {}; update the syntax generator",
            other.to_token_stream()
        ),
    }
}

fn snake_to_lower_camel(value: &str) -> String {
    let mut parts = value.split('_');
    let mut result = parts.next().unwrap_or_default().to_owned();
    for part in parts {
        let mut chars = part.chars();
        if let Some(first) = chars.next() {
            result.extend(first.to_uppercase());
            result.extend(chars);
        }
    }
    result
}

fn serialized<T: Serialize>(value: T) -> Value {
    serde_json::to_value(value).expect("serialize actual Rust DTO")
}

fn sample_for_request_type(type_name: &str) -> Value {
    match type_name {
        "PreviewPasteRequest" => serialized(PreviewPasteRequest {
            raw: "<redacted>".to_owned(),
        }),
        "PreviewRepairRequest" => serialized(PreviewRepairRequest {
            profile_id: "<profileId>".to_owned(),
            raw: "<redacted>".to_owned(),
        }),
        "ConfirmPairingRequest" => serialized(ConfirmPairingRequest {
            preview_id: "<previewId>".to_owned(),
            name: "<name>".to_owned(),
            allow_duplicate_origin: false,
        }),
        "CancelPreviewRequest" => serialized(CancelPreviewRequest {
            preview_id: "<previewId>".to_owned(),
        }),
        "RenameRequest" => serialized(RenameRequest {
            profile_id: "<profileId>".to_owned(),
            new_name: "<newName>".to_owned(),
        }),
        "RemoveRequest" => serialized(RemoveRequest {
            profile_id: "<profileId>".to_owned(),
        }),
        "SelectRequest" => serialized(SelectRequest {
            profile_id: "<profileId>".to_owned(),
        }),
        "PreparedHttpRequest" => serialized(PreparedHttpRequest {
            request_id: "<requestId>".to_owned(),
            active_profile_id: "<profileId>".to_owned(),
            method: "GET".to_owned(),
            path: "/api/example".to_owned(),
            body_length: 0,
            media_type: None,
        }),
        "HubHttpExecuteRequest" => serialized(HubHttpExecuteRequest {
            request_id: "<requestId>".to_owned(),
        }),
        "AppwireOpenRequest" => serialized(AppwireOpenRequest {
            profile_id: "<profileId>".to_owned(),
        }),
        "AppwireSendRequest" => serialized(AppwireSendRequest {
            connection_id: "<connectionId>".to_owned(),
            frame: "<frame>".to_owned(),
        }),
        "AppwireCloseRequest" => serialized(AppwireCloseRequest {
            connection_id: "<connectionId>".to_owned(),
        }),
        unexpected => panic!(
            "new request DTO {unexpected} has no serialized sample; fixture generation is intentionally fail-closed"
        ),
    }
}

fn dto_fixtures() -> Value {
    json!({
        "httpResponseMetadata": HubHttpResponseMetadata {
            request_id: "<requestId>".to_owned(),
            status: 404,
            headers: HubResponseHeaders {
                content_type: Some("image/jpeg".to_owned()),
                content_length: None,
                etag: Some("opaque-etag".to_owned()),
                last_modified: None,
            },
            media_type: Some("image/jpeg".to_owned()),
            body_length: 6,
        },
        "appwireCurrentText": AppwireChannelEvent::Text {
            connection_id: "<profileId>:7".to_owned(),
            profile_id: "<profileId>".to_owned(),
            generation: 7,
            data: "current-frame".to_owned(),
        },
        "appwireStaleText": AppwireChannelEvent::Text {
            connection_id: "<profileId>:6".to_owned(),
            profile_id: "<profileId>".to_owned(),
            generation: 6,
            data: "stale-frame".to_owned(),
        },
    })
}

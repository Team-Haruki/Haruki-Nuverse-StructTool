use std::env;

use avro_parser::{decode, load_file};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut args = env::args().skip(1);
    let mut schema_path = String::new();
    let mut class_name = String::new();
    let mut hex_data = String::new();

    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--schema" => schema_path = args.next().unwrap_or_default(),
            "--class" => class_name = args.next().unwrap_or_default(),
            "--hex" => hex_data = args.next().unwrap_or_default(),
            _ => {}
        }
    }

    if schema_path.is_empty() || class_name.is_empty() || hex_data.is_empty() {
        eprintln!("usage: cargo run -- --schema <schema.avsc> --class <ClassName> --hex <hex>");
        std::process::exit(1);
    }

    let (registry, _) = load_file(&schema_path)?;
    let schema = registry
        .get(&class_name)
        .ok_or_else(|| format!("class not found: {class_name}"))?;
    let bytes = hex::decode(hex_data)?;
    let restored = decode(schema, &bytes, &registry)?;
    println!("{}", serde_json::to_string_pretty(&restored)?);
    Ok(())
}

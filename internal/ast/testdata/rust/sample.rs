use std::collections::HashMap;

pub struct Config {
    pub name: String,
}

pub enum Mode {
    Fast,
    Slow,
}

pub trait Describe {
    fn describe(&self) -> String;
}

pub fn parse(input: &str) -> Result<i32, String> {
    input.parse::<i32>().map_err(|e| e.to_string())
}

fn helper() -> i32 {
    1
}

impl Config {
    pub fn new(name: String) -> Self {
        Config { name }
    }
}

impl Describe for Config {
    fn describe(&self) -> String {
        self.name.clone()
    }
}

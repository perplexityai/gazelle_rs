use model::User;

pub fn render(user: &User) -> String {
    let mut buffer = itoa::Buffer::new();
    format!("{} (#{})", user.name, buffer.format(user.id))
}

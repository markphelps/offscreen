import SwiftUI

@main
struct HelloApp: App {
    var body: some Scene {
        WindowGroup("Hello") {
            ContentView()
        }
    }
}

struct ContentView: View {
    @State private var message = "Ready"

    var body: some View {
        VStack(spacing: 16) {
            Text(message)
                .accessibilityIdentifier("statusLabel")
            Button("Change label") {
                message = "Changed"
            }
        }
        .padding(32)
        .frame(minWidth: 280, minHeight: 160)
    }
}

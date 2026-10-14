import XCTest

final class HelloUITests: XCTestCase {
    func testButtonChangesLabel() {
        let app = XCUIApplication()
        app.launch()

        let status = app.staticTexts["statusLabel"]
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        XCTAssertEqual(text(of: status), "Ready")

        let button = app.buttons["Change label"]
        XCTAssertTrue(button.exists)
        #if os(macOS)
        button.click()
        #else
        button.tap()
        #endif
        XCTAssertEqual(text(of: status), "Changed")
    }

    // macOS exposes a static text's string as its value; iOS exposes it as its label.
    private func text(of element: XCUIElement) -> String {
        #if os(macOS)
        element.value as? String ?? ""
        #else
        element.label
        #endif
    }
}

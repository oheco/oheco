package manager

import "testing"

func TestProjectSDKVersionForms(t *testing.T) {
	for value, want := range map[string]int{"26": 26, "26.0.0": 26, "6.0.2(22)": 22, " 6.0.0(20) ": 20, "6.0.2": 0, "": 0, "unknown": 0} {
		if got := projectAPI(value); got != want {
			t.Errorf("projectAPI(%q) = %d, want %d", value, got, want)
		}
	}
}

func TestProjectDevEcoCLIVersionCompatibility(t *testing.T) {
	for value, want := range map[string]bool{"1.3.5\n": true, "v1.3.5": true, "1.4.0": true, "2.0.0": true, "1.3.4": false, "1.2.99": false, "0.9.0": false, "unavailable": false} {
		if got := compatibleProjectCLI([]byte(value)); got != want {
			t.Errorf("compatibleProjectCLI(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestProjectHDCExplicitInstallationSuccess(t *testing.T) {
	for text, want := range map[string]bool{
		"[Info]App install path:/private/install-123.hap msg:install bundle successfully. \nAppMod finish\n":                              true,
		"[Info]App install path:/private/install-123.hap msg:install bundle successfully.\n":                                              false,
		"msg:install bundle successfully.\nAppMod finish\n":                                                                               false,
		"[Info]App install path:/private/install-123.hap msg:install bundle successfully.\nerror: second package failed\nAppMod finish\n": false,
		"[Success]Install package successfully.\n":                                                                                        true,
		"[Info] Begin install\n[Success] Install package successfully.\n":                                                                 true,
		"[Fail] Could not install; previous operation completed successfully.\n":                                                          false,
		"Success count: 0; installation failed\n":                                                                                         false,
		"install package successfully\n":                                                                                                  false,
		"[Success]Install package successfully.\nError: final installation was rejected\n":                                                false,
	} {
		if got := projectOutputSucceeded([]byte(text)); got != want {
			t.Errorf("projectOutputSucceeded(%q) = %v, want %v", text, got, want)
		}
	}
}

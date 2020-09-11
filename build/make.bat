set GOARCH=arm
set GOOS=linux

go build -o ..\bin\mbgw ..\cmd\mbgw.go
cd

set GOARCH=386
set GOOS=windows

go build -o ..\bin\mbgw.exe ..\cmd\mbgw.go


rem go tool dist install -v pkg/runtime
rem go install -v -a std
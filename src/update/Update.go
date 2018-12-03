package main

import (
	"./scp"
	"./sshConnection"
	"fmt"
	"os"
	"time"
	"os/exec"
	"bytes"
	"io"
	"flag"
	"strings"
)

func getProjectDir(giturl string) string {
	return giturl[strings.LastIndex(giturl, "/")+1:]
}
func getEarRelativePath(srcRoot string) string {
	return "/" + srcRoot + "-ear/target/" + srcRoot + "-ear.ear"
}
func main() {
	parameters := parseArg()
	prepareFileNames(parameters)
	liquibaseCMD := createLiquibaseCmd(parameters)

	localDbLogFileBackup(parameters)

	client := sshConnection.GetClient(parameters)
	runRemoteCmd(&client, remoteDbLogTableDump, parameters)

	copyFromRemote(&client, getValue(parameters, "remote-db-log-file-path"))
	dropLocalDbLogTable(parameters)
	localDbLogTableRestore(parameters, getLocalTmpDir()+getValue(parameters, "remote-db-log-file-path"))
	localPullProject(parameters)

	projectWorkingDir := getValue(parameters, "local-project-dir") + getProjectDir(getValue(parameters, "repo-url"))

	getDbChangesSql(projectWorkingDir, liquibaseCMD)
	dropLocalDbLogTable(parameters)
	localDbLogTableRestore(parameters, getLocalTmpDir()+getValue(parameters, "local-db-log-file-path"))
	buildEAR(projectWorkingDir)

	deploymentDir := "deploy_v" + parameters["version"] + "_" + getValue(parameters, "file-timestamp")
	prepareDeploymentPackage(projectWorkingDir,
		getLocalTmpDir()+deploymentDir,
		getLocalTmpDir()+getValue(parameters, "sql-file"),
		getValue(parameters, "src-root"))
	copyToRemote(&client, getLocalTmpDir(), deploymentDir+".tar.gz")
	clean([]string{
		getLocalTmpDir() + deploymentDir + ".tar.gz",
		getLocalTmpDir() + getValue(parameters, "sql-file"),
		getLocalTmpDir() + getValue(parameters, "local-db-log-file-path"),
		getLocalTmpDir() + getValue(parameters, "remote-db-log-file-path"),
	})
}

func remoteDbLogDump(parameters map[string]string) sshConnection.Command {
	remoteDb := parameters["remote-db-name"]
	remoteDbUser := parameters["remote-db-user"]
	dbLogFileDump := getRemoteTmpDir() + parameters["remote-db-log-file-path"]
	dbPass := parameters["remote-db-pass"]
	dbUrl := getValue(parameters, "remote-db-url")
	dbPort := getValue(parameters, "remote-db-port")
	schema := getValue(parameters, "remote-db-schema")
	cmd := "pg_dump -U " + remoteDbUser + " -d " + remoteDb + " -h " + dbUrl + " -p " + dbPort + " -t " + schema + ".databasechangelog -O -x -f " + dbLogFileDump
	if len(dbPass) > 0 {
		cmd = "export PGPASSWORD='" + dbPass + "';" + cmd
	}
	return sshConnection.Command{Cmd: cmd}
}

func remoteDbLogTableDump(conn sshConnection.ConnectionInt, parameters map[string]string) []func() {
	wildflyPass := getValue(parameters, "wildfly-pass")
	valid := func() {
		conn.Valid()
	}
	loginAsWildfly := func() {
		conn.Execute(sshConnection.Command{Cmd: "su - wildfly"})
		conn.Execute(sshConnection.Command{Cmd: wildflyPass})
	}
	dumpLog := func() {
		conn.Execute(remoteDbLogDump(parameters))
	}
	chmod := func() {
		conn.Execute(sshConnection.Command{Cmd: "chmod 777 " + getRemoteTmpDir() + parameters["remote-db-log-file-path"]})
	}
	exit := func() {
		conn.Execute(sshConnection.Command{Cmd: "exit"})
	}
	return []func(){
		loginAsWildfly, valid, dumpLog, valid, chmod, valid, exit, exit,
	}
}

func localDbLogFileBackup(parameters map[string]string) {
	fmt.Println("local db table backup ...")
	dumpFile := getLocalTmpDir() + getValue(parameters, "local-db-log-file-path")
	user := getValue(parameters, "local-db-user")
	password := getValue(parameters, "local-db-password")
	dbName := getValue(parameters, "local-db-name")
	schema := getValue(parameters, "local-db-schema")
	if len(password) > 0 {
		exec.Command("export PGPASSWORD='" + password + "';").Run()
	}
	cmdC := exec.Command("pg_dump", "-U", user, "-d", dbName, "-t", schema+".databasechangelog", "-O", "-x", "-F", "p", "-f", dumpFile)
	err := cmdC.Run()
	if err != nil {
		fmt.Println("Local table not found")
	}
	fmt.Println("local db table backup completed")
}
func showCommandOutput(cmd *exec.Cmd) {
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err != nil {
		//fmt.Println(out)
		fmt.Println(fmt.Sprint(err) + " :" + stderr.String())
		panic("Cannot execute command" + err.Error())
	}
	output := strings.Trim(out.String(), " ")
	if len(output) > 0 {
		fmt.Println(out.String())
	}
}
func localDbLogTableRestore(parameters map[string]string, file string) {
	fmt.Println("restoring table...")
	user := getValue(parameters, "local-db-user")
	password := getValue(parameters, "local-db-password")
	dbName := getValue(parameters, "local-db-name")
	dbUrl := getValue(parameters, "local-db-url")
	dbPort := getValue(parameters, "local-db-port")
	if len(password) > 0 {
		exec.Command("export PGPASSWORD='" + password + "';").Run()
	}
	fmt.Println(file)
	fmt.Println("restoring table...")
	exec.Command("psql", "-U", user, "-d", dbName, "-h", dbUrl, "-p", dbPort, "-1", "-f", file).Run()
	fmt.Println("restoring table completed")
}
func dropLocalDbLogTable(parameters map[string]string) {
	fmt.Println("drop table log file...")
	dbSchema := getValue(parameters, "local-db-schema")
	psqlCmd := "drop table " + dbSchema + ".databasechangelog"
	user := getValue(parameters, "local-db-user")
	password := getValue(parameters, "local-db-password")
	dbName := getValue(parameters, "local-db-name")
	dbUrl := getValue(parameters, "local-db-url")
	dbPort := getValue(parameters, "local-db-port")
	if len(password) > 0 {
		exec.Command("export PGPASSWORD='" + password + "';").Run()
	}
	cmd := exec.Command("psql", "-U", user, "-d", dbName, "-h", dbUrl, "-p", dbPort, "-c", psqlCmd)
	err := cmd.Run()
	if err != nil {
		fmt.Println("Cannot drop local log table." + err.Error())
	}
	fmt.Println("drop table log file completed")
}

func localPullProject(parameters map[string]string) {
	fmt.Println("Downloading project...")
	dir := parameters["local-project-dir"]
	err := os.MkdirAll(dir, 0777)
	userName := parameters["git-login"]
	password := parameters["git-password"]
	url := parameters["repo-url"]
	branch := parameters["git-branch"]

	if err != nil {
		panic("Cannot create directory." + err.Error())
	}
	repo := fmt.Sprintf("https://%s:%s@%s", userName, password, url)
	cmd := exec.Command("git", "clone", repo)
	cmd.Dir = dir
	showCommandOutput(cmd)
	fmt.Println("Downloading project completed")
	cmd = exec.Command("git", "checkout", "-b", branch, "origin/"+branch)
	cmd.Dir = dir + "/" + getProjectDir(url) + "/"
	showCommandOutput(cmd)
	fmt.Println("Switch branch completed")
}
func getDbChangesSql(projectDir string, cmdArgs []string) {
	fmt.Println("Generating sql diff file...")
	cmd := exec.Command("java", cmdArgs...)
	cmd.Dir = projectDir
	//cmd.Run()
	showCommandOutput(cmd)
	fmt.Println("Generating sql diff file completed")
}
func saveFile(input *scp.File, localFile string) error {
	f, err := os.Create(localFile)

	defer func() {
		if err := f.Close(); err != nil {
			panic(err)
		}
	}()
	buf := make([]byte, 1024)
	for {
		// read a chunk
		n, err := input.Read(buf)
		if err != nil && err != io.EOF {
			panic(err)
		}
		if n == 0 {
			break
		}

		// write a chunk
		if _, err := f.Write(buf[:n]); err != nil {
			panic(err)
		}
	}
	return err
}
func buildEAR(projectDir string) {
	fmt.Println("Building ear...")
	cmd := exec.Command("mvn", "clean", "install")
	cmd.Dir = projectDir
	cmd.Run()
	fmt.Println("Building ear completed")
}
func prepareDeploymentPackage(projectDir, deploymentDir, sqlFile, srcRoot string) {
	fmt.Println("Moving files...")
	err := os.MkdirAll(deploymentDir, 0777)
	if err != nil {
		panic("Directory not created" + err.Error())
	}
	cmd := exec.Command("cp", projectDir+getEarRelativePath(srcRoot), deploymentDir+"/")
	//cmd.Run()
	showCommandOutput(cmd)
	cmd = exec.Command("cp", sqlFile, deploymentDir+"/")
	showCommandOutput(cmd)
	fmt.Println("Created package:" + deploymentDir)
	fmt.Println("Creating archive...")
	archiveFileName := deploymentDir + ".tar.gz"
	err = exec.Command("tar", "-czvf", archiveFileName, deploymentDir).Run()
	if err != nil {
		fmt.Println("Failed")
	} else {
		fmt.Println("Creating archive completed")
		fmt.Println("Created archive:" + archiveFileName)
	}
}
func removeDirectory(dir string) {
	err := os.RemoveAll(dir)
	if err != nil {
		fmt.Println("Removing directory failed" + err.Error())
	}
}
func parseArg() map[string]string {
	ver := flag.String("version", "no-ver", "deployment version")
	//remote conf
	remoteAddress := flag.String("remote-addr", "127.0.0.1", "remote host ip")
	remotePort := flag.String("remote-port", "22", "remote host port")
	remoteDbName := flag.String("remote-db-name", "remoteDBName", "Remote db name")
	remoteDbSchema := flag.String("remote-db-schema", "remoteDBScehma", "Remote db schema")
	remoteDbUser := flag.String("remote-db-user", "remoteDBUser", "Remote db userName")
	remoteDbPassword := flag.String("remote-db-pass", "", "Remote db password")
	remoteDbUrl := flag.String("remote-db-url", "localhost", "Remote db url")
	remoteDbPort := flag.String("remote-db-port", "5432", "Remote db port")
	remoteUserName := flag.String("remote-host-user", "wildfly", "Remote user to login")
	remoteUserPassword := flag.String("remote-host-user-password", "", "Remote user password")
	wildflyPassword := flag.String("remote-host-wildfly-password", "", "Remote user - wildfly password")
	//local conf
	localDbUser := flag.String("local-db-user", "username", "Local db username")
	localDbPassword := flag.String("local-db-password", "", "Local db password")
	localDbName := flag.String("local-db-name", "localDbName", "Local db name")
	localDbSchema := flag.String("local-db-schema", "localDbSchema", "Local db schema name")
	//git conf
	repoUrl := flag.String("repo-url", "git.name.pl/name1/name2", "Repository url without https")
	gitBranch := flag.String("git-branch", "master", "Git branch")
	gitLogin := flag.String("git-user", "username", "Git username")
	gitPassword := flag.String("git-pass", "", "Git password")
	//liquibase conf
	liquibaseJarPath := flag.String("liquibase-path", "D:/liquibase/liquibase.jar", "Path to liquibase")
	dbDriverJar := flag.String("db-driver", "D:/liquibase/postgresql-42.1.4.jar", "Path to db driver")
	localDbUrl := flag.String("local-db-url", "localhost", "Local db url")
	localDbPort := flag.String("local-db-port", "5432", "Local db port")
	context := flag.String("sql-context", "prod", "Liquibase context")
	srcRoot := flag.String("src-root", "directoryName", "Source code root dir")

	usekey := flag.String("use-key", "true", "Use ssh key?")

	flag.Parse()

	return map[string]string{
		"version":                   *ver,
		"git-login":                 *gitLogin,
		"git-password":              *gitPassword,
		"repo-url":                  *repoUrl,
		"remote-addr":               *remoteAddress,
		"remote-port":               *remotePort,
		"remote-db-pass":            *remoteDbPassword,
		"remote-host-user":          *remoteUserName,
		"remote-host-user-password": *remoteUserPassword,
		"wildfly-pass":              *wildflyPassword,
		"remote-db-name":            *remoteDbName,
		"remote-db-schema":          *remoteDbSchema,
		"remote-db-user":            *remoteDbUser,
		"remote-db-url":             *remoteDbUrl,
		"remote-db-port":            *remoteDbPort,

		"local-db-user":     *localDbUser,
		"local-db-password": *localDbPassword,
		"local-db-name":     *localDbName,
		"local-db-schema":   *localDbSchema,
		"liquibase-path":    *liquibaseJarPath,
		"db-driver-jar":     *dbDriverJar,
		"local-db-url":      *localDbUrl,
		"local-db-port":     *localDbPort,
		"sql-context":       *context,
		"use-key":           *usekey,
		"src-root":           *srcRoot,

		"git-branch": *gitBranch,
	}
}
func runRemoteCmd(client *sshConnection.Client,
	cmds func(con sshConnection.ConnectionInt, params map[string]string) []func(),
	params map[string]string) {
	fmt.Println("Remote command...")
	err := client.Connect()
	if err != nil {
		panic("Session not started" + err.Error())
	}
	client.RunCommands(cmds, params)
	client.Close()
	fmt.Println("Remote command completed")
}
func copyFromRemote(client *sshConnection.Client, remoteFile string) {
	fmt.Println("Transfering file from remote...")
	err := client.Connect()
	if err != nil {
		panic("Session not started" + err.Error())
	}
	localFile, err := scp.Read(client, getRemoteTmpDir()+remoteFile)
	if err != nil {
		panic("Transfer file failure" + err.Error())
	}
	err = saveFile(localFile, getLocalTmpDir()+remoteFile)
	if err != nil {
		panic("Transfer file failure" + err.Error())
	}
	fmt.Println("Transfering file from remote completed")
}
func copyToRemote(client *sshConnection.Client, path, file string) {
	fmt.Println("Transfering file to remote host...")
	err := client.Connect()
	if err != nil {
		panic("Session not started" + err.Error())
	}
	scp.CopyLocalToRemote(client, path+file, getRemoteTmpDir()+file)
	client.Close()
	fmt.Println("Transfering file to remote host completed")
	fmt.Println("File avilable at:", getRemoteTmpDir()+file)
}
func clean(localFiles []string) {
	for _, i := range localFiles {
		removeDirectory(i)
	}
}
func getRemoteTmpDir() string {
	return "/tmp/"
}
func getLocalTmpDir() string {
	//return os.TempDir()
	return "C:/tmp/"
}
func prepareFileNames(parameters map[string]string) {
	dateTimeFormat := "2120061545"
	fileTimestamp := time.Now().Format(dateTimeFormat)
	remoteDbLogFileName := "remote_changelog" + fileTimestamp + ".sql"
	localDbLogFileName := "local_changelog" + fileTimestamp + ".sql"
	localProjectDir := getLocalTmpDir() + fileTimestamp
	sqlFile := "UPDATE_" + fileTimestamp + ".sql"

	parameters["remote-db-log-file-path"] = remoteDbLogFileName
	parameters["local-db-log-file-path"] = localDbLogFileName
	parameters["local-project-dir"] = localProjectDir
	parameters["sql-file"] = sqlFile
	parameters["file-timestamp"] = fileTimestamp
}
func getValue(parameters map[string]string, key string) string {
	return parameters[key]
}
func createLiquibaseCmd(parameters map[string]string) []string {
	dbUser := getValue(parameters, "local-db-user")
	dbPassword := getValue(parameters, "local-db-password")
	dbName := getValue(parameters, "local-db-name")
	dbSchema := getValue(parameters, "local-db-schema")
	liquiJar := getValue(parameters, "liquibase-path")
	dbDriver := getValue(parameters, "db-driver-jar")
	dbUrl := getValue(parameters, "local-db-url")
	dbPort := getValue(parameters, "local-db-port")
	context := getValue(parameters, "sql-context")
	sqlFile := getLocalTmpDir() + getValue(parameters, "sql-file")
	return []string{
		"-jar", liquiJar,
		"--driver=org.postgresql.Driver",
		"--classpath=" + dbDriver,
		"--changeLogFile=liquibase\\changelog.xml",
		"--url=jdbc:postgresql://" + dbUrl + ":" + dbPort + "/" + dbName,
		"--username=" + dbUser,
		"--password=" + dbPassword,
		"--defaultSchemaName=" + dbSchema,
		"--contexts=" + context,
		"--outputFile=" + sqlFile,
		"updateSql",
	}
}

func getEnvVariable(name string) string {
	return os.Getenv(name)
}
